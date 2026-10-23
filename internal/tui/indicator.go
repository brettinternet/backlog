package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// loadingDelay is how long work runs before its loading indicator appears,
// and how long a shown indicator stays at least. Faster work paints once, and
// borderline work never flashes an indicator.
const loadingDelay = 300 * time.Millisecond

type loadKind int

const (
	listLoad   loadKind = iota // the list has no current rows to show
	statusLoad                 // a projection or source sync is running
	detailLoad                 // the detail page is loading
	loadKinds
)

// indicator times one kind of work, so its loading indicator shows only once
// a busy period outlasts loadingDelay.
type indicator struct {
	period  uint64 // the busy period being timed
	busy    bool
	shown   bool
	minimum bool // shown less than loadingDelay ago
}

type indicatorDue struct {
	kind    loadKind
	period  uint64
	minimum bool // the shown minimum ended, not the delay
}

// track records whether the work is running, and times a new busy period.
func (i *indicator) track(kind loadKind, busy bool) tea.Cmd {
	if busy == i.busy {
		return nil
	}
	i.busy = busy
	if !busy {
		i.shown = i.shown && i.minimum
		return nil
	}
	if i.shown {
		return nil
	}
	i.period++
	return i.tick(indicatorDue{kind: kind, period: i.period})
}

// due shows the indicator when the delay ends mid-period, and hides it when
// its minimum ends after the work.
func (i *indicator) due(msg indicatorDue) tea.Cmd {
	if msg.period != i.period {
		return nil
	}
	if msg.minimum {
		i.minimum = false
		i.shown = i.busy
		return nil
	}
	if !i.busy {
		return nil
	}
	i.shown, i.minimum = true, true
	msg.minimum = true
	return i.tick(msg)
}

func (i *indicator) tick(msg indicatorDue) tea.Cmd {
	return tea.Tick(loadingDelay, func(time.Time) tea.Msg { return msg })
}
