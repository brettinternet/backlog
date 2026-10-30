package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/tour"
)

// TourResult is where a stored tour attaches.
type TourResult struct {
	Item   string `json:"item"`
	Change string `json:"change"`
	Ref    string `json:"ref"`
	Steps  int    `json:"steps"`
}

func init() {
	// hooks can't import engine, so an action or hook's {"tour": PATH}
	// result reaches this intake through the hooks package.
	hooks.TourIntake = func(ctx context.Context, cfg project.Config, ref, path string) error {
		_, err := StoreTourFile(ctx, cfg, ref, path)
		return err
	}
}

// StoreTourFile reads the tour file at path and stores it as StoreTour does.
func StoreTourFile(ctx context.Context, cfg project.Config, id, path string) (TourResult, error) {
	data, err := tour.ReadFile(path)
	if err != nil {
		return TourResult{}, source.Refuse("rejected", "tour: "+err.Error())
	}
	return StoreTour(ctx, cfg, id, data)
}

// StoreTour validates a tour an agent wrote for item id and keeps a copy for
// the linked change whose head is the tour's ref; bl never writes a tour of
// its own (design.md D43). Every intake (bl tour, MCP tour, and an action or
// hook result) comes through here.
func StoreTour(ctx context.Context, cfg project.Config, id string, data []byte) (TourResult, error) {
	t, err := tour.Parse(data)
	if err != nil {
		return TourResult{}, source.Refuse("rejected", err.Error())
	}
	items, err := ReadDetail(ctx, cfg, id)
	if err != nil {
		return TourResult{}, err
	}
	if len(items) == 0 {
		return TourResult{}, source.Refuse("not-found", "item not found: "+id)
	}
	if len(items) != 1 {
		return TourResult{}, source.Refuse("rejected", "ambiguous item ID: "+id)
	}
	item := items[0]
	if stateDir, _, _ := tourKey(cfg, item.Ref); !filepath.IsAbs(stateDir) {
		return TourResult{}, source.Refuse("unavailable", "no state directory for tours")
	}
	for i, s := range t.Steps {
		if s.Criterion > len(item.Criteria) {
			return TourResult{}, source.Refuse("rejected", "tour step "+strconv.Itoa(i+1)+": "+item.Ref.ID+" has no acceptance criterion "+strconv.Itoa(s.Criterion))
		}
	}
	linked, err := ItemChanges(ctx, cfg, item.Ref)
	if err != nil {
		return TourResult{}, err
	}
	var heads []string
	for _, c := range linked.Changes {
		if c.Head != t.Ref {
			if c.Head != "" {
				heads = append(heads, c.Head[:min(7, len(c.Head))]+" ("+c.ID+")")
			}
			continue
		}
		if err := checkSteps(ctx, c, t); err != nil {
			return TourResult{}, err
		}
		stateDir, projectID, key := tourKey(cfg, item.Ref)
		stored := tour.Stored{Item: key, Change: c.ID, Stored: time.Now().UTC(), Tour: t}
		if err := tour.Store(stateDir, projectID, stored); err != nil {
			return TourResult{}, err
		}
		return TourResult{Item: item.Ref.Source + ":" + item.Ref.ID, Change: c.ID, Ref: t.Ref, Steps: len(t.Steps)}, nil
	}
	reason := "tour ref " + t.Ref[:7] + " is the head of no change linked to " + item.Ref.ID
	if len(heads) > 0 {
		reason += "; linked heads: " + strings.Join(heads, ", ")
	}
	return TourResult{}, source.Refuse("rejected", reason)
}

// checkSteps refuses a tour with a step on a file or line its change's Diff
// lacks, listing every such step so an agent can fix them in one pass. It
// reads Git objects at the tour's ref, or for an old-side step the commit the
// Diff compares it with, never a worktree. A step it can't check, on a head
// the checkout doesn't hold or an old side it can't find, is accepted.
func checkSteps(ctx context.Context, c changes.Change, t tour.Tour) error {
	sides, ok, err := changes.ReadSides(ctx, c, t.Ref)
	if err != nil || !ok {
		return err
	}
	type side struct {
		path string
		old  bool
	}
	type file struct {
		lines   int
		problem string
		checked bool
	}
	files := map[side]file{}
	var bad []string
	for i, s := range t.Steps {
		at := side{s.File, s.Side == tour.SideOld}
		where := "at " + t.Ref[:7]
		if at.old {
			where = "before the change"
		}
		f, seen := files[at]
		if !seen {
			content, ok, err := sides.File(ctx, s.File, at.old)
			switch {
			case !ok:
			case errors.Is(err, os.ErrNotExist):
				f.checked, f.problem = true, "no such file "+where
			case err != nil:
				f.checked, f.problem = true, err.Error()
			default:
				f.checked, f.lines = true, bytes.Count(content, []byte("\n"))
				if len(content) > 0 && content[len(content)-1] != '\n' {
					f.lines++
				}
			}
			files[at] = f
		}
		problem := f.problem
		if f.checked && problem == "" && s.Line > f.lines {
			problem = "past the end of the file, which has " + strconv.Itoa(f.lines) + " lines " + where
		}
		if problem != "" {
			bad = append(bad, "step "+strconv.Itoa(i+1)+": "+s.File+" line "+strconv.Itoa(s.Line)+": "+problem)
		}
	}
	if len(bad) > 0 {
		return source.Refuse("rejected", "tour "+strings.Join(bad, "; "))
	}
	return nil
}

// LoadTour reads the tour kept for an item's change, on a worker.
func LoadTour(cfg project.Config, ref model.Ref, change string) (tour.Stored, bool, error) {
	stateDir, projectID, key := tourKey(cfg, ref)
	if !filepath.IsAbs(stateDir) {
		return tour.Stored{}, false, nil
	}
	return tour.Load(stateDir, projectID, key, change)
}

// tourKey is where an item's tours are kept: its own project's state, under
// its source's ID there, so a workspace and the project agree.
func tourKey(cfg project.Config, ref model.Ref) (stateDir, projectID, item string) {
	p, local := cfg.SourceProject(ref.Source)
	return p.Paths.StateDir, p.Project.ID, local + ":" + ref.ID
}
