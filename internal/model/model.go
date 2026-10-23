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
	Restore          Operation = "restore"
)

// Mutation targets a field or section. Index is one-based. Apply binds an
// inverse to the resulting version; it cannot overwrite subsequent edits.
type Mutation struct {
	Ref             Ref
	ExpectedVersion string
	Op              Operation
	Field           string
	Value           any
	Section, Text   string
	Index           int
	Evidence        *string    // nil preserves evidence on check; uncheck always removes it
	Changes         []Mutation // atomic same-item Batch
	Comment         Comment
	Original        []byte // only for a version-bound Restore returned by the codec
	Item            *Item  // template for Create; the source allocates its ID
}
