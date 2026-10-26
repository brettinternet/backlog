package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	urfave "github.com/urfave/cli/v3"
)

func claimCommands(directory *string, in io.Reader, out, errOut io.Writer) []*urfave.Command {
	commands := []*urfave.Command{}
	for _, c := range [][3]string{
		{"claim", "Claim an item for this session", "ID"},
		{"release", "Release this session's claim on an item", "ID"},
		{"heartbeat", "Renew this session's claim, or keep renewing it while COMMAND runs", "[-- COMMAND [ARGS...]]"},
	} {
		commands = append(commands, &urfave.Command{Name: c[0], Usage: c[1], ArgsUsage: c[2], Category: categoryCoordination, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Name != "heartbeat" && cmd.Args().Len() != 1 {
				return writeUsage(cmd.Name + " requires exactly one ID")
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			var result model.ClaimResult
			var label string
			if cmd.Name == "heartbeat" {
				result = engine.Heartbeat(ctx)
				if result.Reason == "" && cmd.Args().Len() != 0 {
					return renewWhile(ctx, result.ExpiresAt, cmd.Args().Slice(), in, out, errOut)
				}
			} else {
				item, e := writeItem(ctx, cfg, cmd.Args().First())
				if e != nil {
					return e
				}
				label = refsFor(cfg).ref(item.Ref, item.Ref.Source) + " "
				if cmd.Name == "claim" {
					result = engine.ClaimItem(ctx, item)
				} else {
					result = engine.ReleaseItem(ctx, item)
				}
			}
			if result.Reason != "" {
				return &Error{Reason: result.Reason, Message: result.Message, Data: result}
			}
			if cmd.Bool("json") {
				return success(out, cmd.Name, result)
			}
			text := label + string(result.Outcome)
			if !result.ExpiresAt.IsZero() {
				text += " until " + result.ExpiresAt.Local().Format(time.DateTime)
			}
			return writeLine(out, text)
		}})
	}
	return commands
}

// exitStatus carries a child's exit code through Run without an error message.
type exitStatus int

func (e exitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// renewWhile runs argv and renews the claim at half its remaining lease until
// argv exits, so a long blocking command cannot outlive the claim. A failed
// renewal warns and retries sooner; once the lease has lapsed it stops renewing.
// The child is never stopped for a renewal failure.
func renewWhile(ctx context.Context, expires time.Time, argv []string, in io.Reader, out, errOut io.Writer) error {
	child := exec.CommandContext(ctx, argv[0], argv[1:]...)
	child.Stdin, child.Stdout, child.Stderr = in, out, errOut
	child.Cancel = func() error { return child.Process.Signal(os.Interrupt) }
	child.WaitDelay = 10 * time.Second
	if err := child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	renewing := true
	for {
		var tick <-chan time.Time
		if renewing && ctx.Err() == nil {
			tick = time.After(max(time.Until(expires)/2, time.Second))
		}
		select {
		case err := <-done:
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					return exitStatus(128 + int(status.Signal()))
				}
				return exitStatus(exit.ExitCode())
			}
			return err
		case <-tick:
			result := engine.Heartbeat(ctx)
			if result.Reason == "" {
				expires = result.ExpiresAt
				continue
			}
			fmt.Fprintf(errOut, "bl: warning: heartbeat failed: %s\n", result.Message)
			if time.Now().After(expires) {
				fmt.Fprintln(errOut, "bl: warning: claim lapsed; no longer renewing")
				renewing = false
			}
		}
	}
}
