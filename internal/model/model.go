// Package model defines source-independent backlog items and edits.
package model

import "time"

type Ref struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}
type StatusCategory string

const (
	Backlog  StatusCategory = "backlog"
	Todo     StatusCategory = "todo"
	Active   StatusCategory = "active"
	Done     StatusCategory = "done"
	Canceled StatusCategory = "canceled"
)

type Priority string

const (
	Urgent Priority = "urgent"
	High   Priority = "high"
	Medium Priority = "medium"
	Low    Priority = "low"
)

type EdgeKind string

const (
	Parent      EdgeKind = "parent"
	BlockedBy   EdgeKind = "blocked_by"
	Related     EdgeKind = "related"
	DuplicateOf EdgeKind = "duplicate_of"
)

type Edge struct {
	Kind   EdgeKind
	Target Ref
}
type Criterion struct {
	Text     string `json:"text"`
	Checked  bool   `json:"checked"`
	Evidence string `json:"evidence,omitempty"`
}
type Comment struct {
	Time         time.Time
	Author, Body string
}
type Item struct {
	Project                              string // Present in composed workspace views, never written to providers.
	Ref                                  Ref
	Title, Type, Status                  string
	Category                             StatusCategory
	Priority                             Priority
	Rank                                 string
	Assignees, Labels                    []string
	Milestone, Due                       string
	Sprint                               string // Stable provider key; resolve state through a Sprint catalog.
	Parent                               *Ref
	Children, BlockedBy, Blocks, Related []Ref
	DuplicateOf                          *Ref
	Edges                                []Edge
	Refs, Files                          []string
	Description                          string
	Criteria, DoD                        []Criterion
	Plan, Notes, Summary                 string
	Comments                             []Comment
	Created, Updated                     time.Time
	URL, Version                         string
	Fields                               map[string]any
	// Names maps opaque Status, Assignees and Sprint values to provider display
	// names. Display only: queries, @me, claims and writes use the values.
	Names map[string]string
}

// SprintState is the sprint's own lifecycle, not the item's. Providers report
// it in a catalog refreshed every sync, because it changes without bumping
// any issue's updated time.
type SprintState string

const (
	SprintActive SprintState = "active"
	SprintFuture SprintState = "future"
	SprintClosed SprintState = "closed"
)

// Sprint is one catalog entry: a Linear cycle or a Jira sprint.
type Sprint struct {
	Key    string      `json:"key"`
	Name   string      `json:"name"`
	State  SprintState `json:"state"`
	Start  time.Time   `json:"start,omitzero"`
	End    time.Time   `json:"end,omitzero"`
	Source string      `json:"source"`
}

// Capabilities is deny-by-default: absent operations are unavailable.
type Capabilities map[Operation]bool

func (c Capabilities) Allows(op Operation) bool { return c[op] }

type Operation string

const (
	Batch            Operation = "batch"
	Create           Operation = "create"
	Archive          Operation = "archive"
	Unarchive        Operation = "unarchive"
	Set              Operation = "set"
	Add              Operation = "add"
	Remove           Operation = "remove"
	Unset            Operation = "unset"
	ReplaceSection   Operation = "replace-section"
	AppendNote       Operation = "append-note"
	AppendComment    Operation = "append-comment"
	CheckCriterion   Operation = "check-criterion"
	UncheckCriterion Operation = "uncheck-criterion"
	AddCriterion     Operation = "add-criterion"
	RemoveCriterion  Operation = "remove-criterion"
	SetCriterion     Operation = "set-criterion"
	Restore          Operation = "restore"
	// Delete undoes a create on a source without archive. Only engine undo
	// sends it. Apply returns an item without a version when it deleted the
	// item, or the item it kept instead.
	Delete Operation = "delete"
)

// Mutation targets a field or section. Index is one-based. Apply binds an
// inverse to the resulting version; it cannot overwrite subsequent edits.
type Mutation struct {
	Ref             Ref
	ExpectedVersion string
	// ExpectedSprint is the sprint key the caller observed ("" for none).
	// Versions exclude the sprint (D31), so sprint writes check it instead.
	ExpectedSprint *string
	Confirmed      bool // Explicit acknowledgement for destructive provider replacements.
	Op             Operation
	Field          string
	Value          any
	Section, Text  string
	Index          int
	Evidence       *string    // nil preserves evidence on check; uncheck always removes it
	Changes        []Mutation // atomic same-item Batch
	Comment        Comment
	Original       []byte // only for a version-bound Restore returned by the codec
	Item           *Item  // template for Create; the source allocates its ID
}
