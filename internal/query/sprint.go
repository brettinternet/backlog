package query

import (
	"cmp"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
)

// Sprint roles are resolved from the snapshot catalog, never the wall clock,
// so a sprint starting or closing changes results without touching items.
const (
	sprintCurrent = "current"
	sprintNext    = "next"
	sprintFuture  = "future"
	sprintClosed  = "closed"
)

var sprintValues = []string{sprintCurrent, sprintNext, "none"}

// SprintCapable reports whether any configured source can supply sprints.
func (s Snapshot) SprintCapable() bool {
	for _, source := range s.Settings.Sources {
		if source.Type == "linear" || source.Type == "jira" {
			return true
		}
	}
	return false
}

// Sprint returns the catalog entry for key and its role: current (active),
// next (the earliest future sprint of its source), future, or closed.
func (s Snapshot) Sprint(key string) (model.Sprint, string, bool) {
	if key == "" {
		return model.Sprint{}, "", false
	}
	for _, sprint := range s.Sprints {
		if sprint.Key != key {
			continue
		}
		switch sprint.State {
		case model.SprintActive:
			return sprint, sprintCurrent, true
		case model.SprintFuture:
			for _, other := range s.Sprints {
				if other.Source == sprint.Source && other.State == model.SprintFuture && compareFuture(other, sprint) < 0 {
					return sprint, sprintFuture, true
				}
			}
			return sprint, sprintNext, true
		}
		return sprint, sprintClosed, true
	}
	return model.Sprint{}, "", false
}

// Undated future sprints sort after dated ones.
func compareFuture(a, b model.Sprint) int {
	if a.Start.IsZero() != b.Start.IsZero() {
		if a.Start.IsZero() {
			return 1
		}
		return -1
	}
	if c := a.Start.Compare(b.Start); c != 0 {
		return c
	}
	return strings.Compare(a.Key, b.Key)
}

func matchSprint(i model.Item, s Snapshot, text string) bool {
	switch strings.ToLower(text) {
	case "none":
		return i.Sprint == ""
	case sprintCurrent, sprintNext:
		_, role, _ := s.Sprint(i.Sprint)
		return role == strings.ToLower(text)
	}
	if i.Sprint == "" {
		return false
	}
	if i.Sprint == text {
		return true
	}
	sprint, _, ok := s.Sprint(i.Sprint)
	return ok && strings.EqualFold(sprint.Name, text)
}

// compareSprintGroups orders current, next, other future sprints by start,
// closed sprints most recent first, uncataloged keys, then none.
func (s Snapshot) compareSprintGroups(a, b string) int {
	rank := func(key string) (int, model.Sprint) {
		if key == "" {
			return 5, model.Sprint{}
		}
		sprint, role, ok := s.Sprint(key)
		if !ok {
			return 4, model.Sprint{Key: key}
		}
		return map[string]int{sprintCurrent: 0, sprintNext: 1, sprintFuture: 2, sprintClosed: 3}[role], sprint
	}
	ar, as := rank(a)
	br, bs := rank(b)
	if c := cmp.Compare(ar, br); c != 0 {
		return c
	}
	switch ar {
	case 1, 2:
		return compareFuture(as, bs)
	case 3:
		if c := bs.End.Compare(as.End); c != 0 {
			return c
		}
	}
	if c := strings.Compare(as.Name, bs.Name); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
