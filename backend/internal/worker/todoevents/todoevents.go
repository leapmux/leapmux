// Package todoevents reduces a heterogeneous stream of agent messages
// (Claude TodoWrite/TaskCreate/TaskUpdate/TaskGet/TaskList, Codex
// turn/plan/updated, ACP sessionUpdate=plan) into a provider-neutral
// to-do list. The worker owns the canonical state in agent_todos and
// broadcasts the post-mutation snapshot to clients via
// AgentTodosChanged; the frontend does not reduce these events
// locally.
package todoevents

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MaxTodos caps the size of an agent's to-do list shipped to clients
// and held in memory by the reducer. Practically, Claude's Task* tool
// rarely produces more than a few dozen rows per agent; the cap is a
// guardrail against a runaway agent flooding `agent_todos` and making
// every cold-start payload pathologically large.
const MaxTodos = 64

// Item mirrors leapmuxv1.TodoItem in a plain-Go shape so the reducer
// stays decoupled from the proto generated types.
type Item struct {
	ID          string
	Content     string
	Status      Status
	ActiveForm  string
	Description string
}

// Status is the canonical to-do status. Mirrors leapmuxv1.TodoStatus
// with friendlier zero-value semantics (zero == pending instead of
// "unspecified"). StatusDeleted is a tombstone: KindDelete events set
// it instead of removing the row, so the chat thread can keep
// rendering the deletion event and the sidebar can show the deleted
// row with a distinct visual. Cap eviction treats StatusCompleted and
// StatusDeleted as a single "terminal" pool.
type Status int

const (
	StatusPending Status = iota
	StatusInProgress
	StatusCompleted
	StatusDeleted
)

// IsTerminal reports whether s is a terminal status — one that makes a
// row eligible for cap-eviction (Completed | Deleted). Pending and
// InProgress rows are never evicted; they only leave the list through
// an explicit Delete event.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusDeleted
}

// Patch carries the fields of a KindUpdate event. Each *string is nil
// for "no change", non-nil (even if empty) for "set to this value" —
// matching TypeScript's `Partial<TodoItem>` semantics. Status is
// represented by a *Status for the same reason.
type Patch struct {
	Content     *string
	ActiveForm  *string
	Description *string
	Status      *Status
}

// EventKind discriminates the five event variants.
type EventKind int

const (
	// KindSnapshot replaces the whole list (TodoWrite, Codex plan,
	// ACP plan, Claude TaskList).
	KindSnapshot EventKind = iota
	// KindCreate appends one row (or replaces by ID for idempotent
	// replay of Claude TaskCreate).
	KindCreate
	// KindUpdate merges fields into the row identified by ID
	// (Claude TaskUpdate).
	KindUpdate
	// KindDelete removes the row identified by ID (Claude TaskUpdate
	// with status="deleted").
	KindDelete
	// KindDetail merges fields into the row identified by ID,
	// appending it when unseen (Claude TaskGet).
	KindDetail
)

// Event is the discriminated union of mutation variants. Fields are
// populated based on Kind; readers must switch on Kind before reading.
type Event struct {
	Kind     EventKind
	Snapshot []Item // KindSnapshot
	Item     Item   // KindCreate / KindDetail (full row)
	ID       string // KindUpdate / KindDelete (target id)
	Patch    Patch  // KindUpdate
}

// ApplyPatch overlays a Patch onto base; nil fields preserve base.
// Used by the worker's persistence layer to apply incremental
// TaskUpdate mutations.
func ApplyPatch(base Item, patch Patch) Item {
	out := base
	if patch.Content != nil {
		out.Content = *patch.Content
	}
	if patch.ActiveForm != nil {
		out.ActiveForm = *patch.ActiveForm
	}
	if patch.Description != nil {
		out.Description = *patch.Description
	}
	if patch.Status != nil {
		out.Status = *patch.Status
	}
	return out
}

// MergeDetail overlays the non-zero fields of detail onto base.
// KindDetail carries a TaskGet snapshot of the row; missing fields
// map to empty strings via StatusFromWire / json zero values, which
// we treat as "preserve". StatusPending is the zero value of Status
// and the default for an empty wire string, so we preserve base on
// that case too — TaskGet is a read-only query and never legitimately
// downgrades a row from in_progress/completed back to pending. Real
// status transitions arrive via KindUpdate (TaskUpdate).
func MergeDetail(base, detail Item) Item {
	out := base
	if detail.Content != "" {
		out.Content = detail.Content
	}
	if detail.ActiveForm != "" {
		out.ActiveForm = detail.ActiveForm
	}
	if detail.Description != "" {
		out.Description = detail.Description
	}
	if detail.Status != StatusPending {
		out.Status = detail.Status
	}
	return out
}

// ToProto converts an in-memory Item to the wire-format proto message.
func (i Item) ToProto() *leapmuxv1.TodoItem {
	return &leapmuxv1.TodoItem{
		Id:          i.ID,
		Content:     i.Content,
		Status:      statusToProto(i.Status),
		ActiveForm:  i.ActiveForm,
		Description: i.Description,
	}
}

// ItemsToProto bulk-converts a slice for proto-shaped responses.
func ItemsToProto(items []Item) []*leapmuxv1.TodoItem {
	out := make([]*leapmuxv1.TodoItem, len(items))
	for i, it := range items {
		out[i] = it.ToProto()
	}
	return out
}

// StatusWire returns the lowercase wire-format string used by the
// agent_todos.status column and the TS reducer ("pending" |
// "in_progress" | "completed" | "deleted").
func StatusWire(s Status) string {
	switch s {
	case StatusInProgress:
		return "in_progress"
	case StatusCompleted:
		return "completed"
	case StatusDeleted:
		return "deleted"
	default:
		return "pending"
	}
}

// StatusFromWire parses the lowercase wire-format string; unknown
// values fall through to StatusPending.
func StatusFromWire(s string) Status {
	switch s {
	case "in_progress", "inProgress":
		return StatusInProgress
	case "completed":
		return StatusCompleted
	case "deleted":
		return StatusDeleted
	default:
		return StatusPending
	}
}

func statusToProto(s Status) leapmuxv1.TodoStatus {
	switch s {
	case StatusInProgress:
		return leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS
	case StatusCompleted:
		return leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED
	case StatusDeleted:
		return leapmuxv1.TodoStatus_TODO_STATUS_DELETED
	default:
		return leapmuxv1.TodoStatus_TODO_STATUS_PENDING
	}
}
