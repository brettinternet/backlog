package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
)

func eventsCommand(directory *string, out io.Writer) *urfave.Command {
	return &urfave.Command{Name: "events", Usage: "Print or follow project events", ArgsUsage: "[QUERY]", Category: categoryAutomation, Flags: []urfave.Flag{
		&urfave.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "Watch sources and stream new events"},
		&urfave.StringFlag{Name: "since", Usage: "Start after event `ID`"},
	}, Action: func(ctx context.Context, cmd *urfave.Command) error {
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		q, err := query.Parse(strings.Join(cmd.Args().Slice(), " "), query.Options{Hook: true})
		if err != nil {
			return &Error{Reason: "usage", Message: err.Error()}
		}
		if _, err = engine.LoadReads(ctx, cfg, false); err != nil {
			if auth := oauthCLIError(err); auth != nil {
				return auth
			}
			return &Error{Reason: "unavailable", Message: err.Error()}
		}
		cursor := cmd.String("since")
		match := engine.EventMatcher(cfg, q)
		emit := func() error {
			page, err := engine.ReadEvents(ctx, cfg, cursor, match, 0)
			if err != nil {
				return writeError(err)
			}
			cursor = page.Cursor
			if !cmd.Bool("follow") && cmd.Bool("json") {
				return success(out, "events", struct {
					Events []hooks.Event `json:"events"`
				}{page.Events})
			}
			for _, e := range page.Events {
				if cmd.Bool("follow") && cmd.Bool("json") {
					err = success(out, "events", struct {
						Event hooks.Event `json:"event"`
					}{e})
				} else {
					_, err = fmt.Fprintf(out, "%s %s %s\n", e.ID, textutil.Line(e.Type), textutil.Line(e.Ref))
				}
				if err != nil {
					return err
				}
			}
			return nil
		}
		if !cmd.Bool("follow") {
			return emit()
		}
		store, err := engine.OpenProjectStore(ctx, cfg)
		if err != nil {
			return err
		}
		defer store.Close()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err = emit(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}}
}
