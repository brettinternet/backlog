// Package model defines source-independent backlog items and edits.
package model

import "time"

type Ref struct {
	Source string
	ID     string
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
	Text    string
	Checked bool
}
type Comment struct {
	Time         time.Time
	Author, Body string
}
type Item struct {
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
	Comment         Comment
	Original        []byte // only for a version-bound Restore returned by the codec
}
