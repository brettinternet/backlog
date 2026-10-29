package engine

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// ResumeChoice is one recorded agent session of an item and the command that
// would reopen it (design.md D40). Reason names the failed check of a refused
// choice: other-host, checkout-removed, live (a held claim or a contact
// channel), no-agent-config, or rejected for a placeholder that fails D21.
type ResumeChoice struct {
	Session    model.ItemSession      `json:"session"`
	Command    string                 `json:"command"`
	Detach     bool                   `json:"detach"`
	Reason     string                 `json:"reason,omitempty"`
	Disabled   string                 `json:"disabled,omitempty"`
	Invocation hooks.ActionInvocation `json:"-"`
}

// ResumeChoices lists the item's agent sessions, newest first, with each one's
// command and refusal. command is resume, fork, or empty to choose: a closed
// item forks when its agent has fork, so a finished transcript isn't extended
// against code that moved on. TUI sessions are never listed. Callers run this
// on a worker, never on the TUI update loop.
func ResumeChoices(ctx context.Context, cfg project.Config, id, command string) ([]ResumeChoice, error) {
	return resumeChoices(ctx, cfg, id, "", command)
}

// SessionChoices offers both resume and fork for one of the item's recorded
// sessions, as History's session rows do. Like ResumeChoices, it runs on a
// worker.
func SessionChoices(ctx context.Context, cfg project.Config, id, session string) ([]ResumeChoice, error) {
	return resumeChoices(ctx, cfg, id, session, "resume", "fork")
}

func resumeChoices(ctx context.Context, cfg project.Config, id, session string, commands ...string) ([]ResumeChoice, error) {
	items, err := ReadDetail(ctx, cfg, id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, source.Refuse("not-found", "item not found: "+id)
	}
	if len(items) != 1 {
		return nil, source.Refuse("rejected", "ambiguous item ID: "+id)
	}
	item := items[0]
	c, _ := Coordination(ctx)
	resource := ""
	if c != nil {
		resource, _ = c.Resource(item)
	}
	claims, claimsErr := ClaimHistory(ctx, c, resource)
	sessions, _ := ItemSessions(ctx, cfg, item, claims)
	closed := item.Category == model.Done || item.Category == model.Canceled
	now := time.Now()
	var choices []ResumeChoice
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, "tui:") || s.Agent == "" && s.AgentSession == "" || session != "" && s.ID != session {
			continue
		}
		agent := cfg.Agents[s.Agent]
		for _, name := range commands {
			if name == "" {
				name = "resume"
				if closed && len(agent.Fork) > 0 {
					name = "fork"
				}
			}
			choice := ResumeChoice{Session: s, Command: name, Detach: agent.Detach}
			run := agent.Resume
			if name == "fork" {
				run = agent.Fork
			}
			switch {
			case s.Agent == "" || s.AgentSession == "" || len(run) == 0:
				choice.Reason, choice.Disabled = "no-agent-config", fmt.Sprintf("no %s command for agent %q", name, cmp.Or(s.Agent, "unknown"))
			case s.Host != hostname():
				choice.Reason, choice.Disabled = "other-host", "session ran on "+cmp.Or(s.Host, "an unknown host")
			case !isDir(s.CWD):
				choice.Reason, choice.Disabled = "checkout-removed", "checkout no longer exists: "+cmp.Or(s.CWD, "unknown")
			case live(s, now):
				choice.Reason, choice.Disabled = "live", "session still holds its claim; message it instead"+channels(s)
			case channels(s) != "":
				choice.Reason, choice.Disabled = "live", "agent is still running; message it instead"+channels(s)
			case claimsErr != "":
				// Without claim state, the session may still be running.
				choice.Reason, choice.Disabled = "live", "claim state unavailable: "+claimsErr
			}
			event := makeEvent(ctx, cfg, item, "", "action", []string{rand.Text()})
			event.ID = rand.Text()
			event.SchemaVersion = 1
			event.Time = now.UTC()
			event.Name = name
			recorded := hooks.Session{ID: s.ID, Agent: s.Agent, AgentSession: s.AgentSession, Host: s.Host, CWD: s.CWD, Branch: s.Branch}
			event.Session = &recorded
			choice.Invocation = hooks.ActionInvocation{Name: name, Event: event, CWD: s.CWD, Agent: &recorded}
			if choice.Reason == "" {
				if err := hooks.CheckAction(cfg, choice.Invocation); err != nil {
					choice.Reason, choice.Disabled = "rejected", err.Error()
					if refusal, ok := err.(*source.Error); ok {
						choice.Reason, choice.Disabled = refusal.Reason, refusal.Message
					}
				}
			}
			choices = append(choices, choice)
		}
	}
	slices.SortStableFunc(choices, func(a, b ResumeChoice) int { return b.Session.LastSeen.Compare(a.Session.LastSeen) })
	return choices, nil
}

// SelectResume picks the session to reopen: the one named by ID or agent
// session, else the only resumable one. A refused choice fails with its check
// as the reason.
func SelectResume(choices []ResumeChoice, session string) (ResumeChoice, error) {
	if session != "" {
		for _, choice := range choices {
			if choice.Session.ID == session || choice.Session.AgentSession == session {
				if choice.Reason != "" {
					return choice, source.Refuse(choice.Reason, choice.Disabled)
				}
				return choice, nil
			}
		}
		return ResumeChoice{}, source.Refuse("not-found", "no recorded agent session "+session)
	}
	var open []ResumeChoice
	for _, choice := range choices {
		if choice.Reason == "" {
			open = append(open, choice)
		}
	}
	switch {
	case len(open) == 1:
		return open[0], nil
	case len(open) > 1:
		return ResumeChoice{}, source.Refuse("usage", "several resumable sessions; pass SESSION")
	case len(choices) == 0:
		return ResumeChoice{}, source.Refuse("not-found", "no recorded agent sessions")
	}
	// Every session is refused: report the newest one's check.
	return choices[0], source.Refuse(choices[0].Reason, choices[0].Disabled)
}

// live reports a session whose claim on the item is held and unexpired: two
// processes would write one transcript.
func live(s model.ItemSession, now time.Time) bool {
	c := s.Claim
	return c != nil && c.EndedAt.IsZero() && (c.ExpiresAt.IsZero() || c.ExpiresAt.After(now))
}

// channels lists the session's contact channels (BL-142) as " via CHANNEL
// HANDLE, …", or "". A resolver reports a channel only while the agent runs.
func channels(s model.ItemSession) string {
	var list []string
	for _, result := range s.Contact {
		for _, c := range result.Channels {
			list = append(list, c.Channel+" "+c.Handle)
		}
	}
	if len(list) == 0 {
		return ""
	}
	return " via " + strings.Join(list, ", ")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return path != "" && err == nil && info.IsDir()
}
