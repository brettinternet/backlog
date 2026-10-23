package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
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
		log := hooks.New(cfg.Paths.StateDir, cfg.Project.ID)
		cursor := cmd.String("since")
		snapshot := query.Snapshot{Settings: cfg.Settings, Me: []string{cfg.Me.Default}, States: map[model.Ref]query.State{}}
		match := func(e hooks.Event) bool {
			var item model.Item
			data, _ := json.Marshal(e.Item)
			_ = json.Unmarshal(data, &item)
			snapshot.Now = e.Time
			state := query.State{}
			if change, ok := e.Changes["readiness"]; ok {
				state.Readiness = fmt.Sprint(change.To)
			}
			if change, ok := e.Changes["archived"]; ok {
				state.Archived = change.To == true
			}
			snapshot.States[item.Ref] = state
			event := query.Event{Type: e.Type, Outcome: e.Outcome}
			if e.Type == "hook.finished" {
				event.Hook = e.Name
			}
			if e.Type == "action.finished" {
				event.Action = e.Name
			}
			for field := range e.Changes {
				event.Changed = append(event.Changed, field)
			}
			return q.Match(item, snapshot, &event)
		}
		emit := func() error {
			events, err := log.Read(ctx, cursor)
			if err != nil {
				return &Error{Reason: "unavailable", Message: err.Error()}
			}
			matched := make([]hooks.Event, 0, len(events))
			for _, e := range events {
				cursor = e.ID
				if !match(e) {
					continue
				}
				matched = append(matched, e)
				if cmd.Bool("follow") {
					if cmd.Bool("json") {
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
			}
			if !cmd.Bool("follow") {
				if cmd.Bool("json") {
					return success(out, "events", struct {
						Events []hooks.Event `json:"events"`
					}{matched})
				}
				for _, e := range matched {
					if _, err = fmt.Fprintf(out, "%s %s %s\n", e.ID, textutil.Line(e.Type), textutil.Line(e.Ref)); err != nil {
						return err
					}
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
