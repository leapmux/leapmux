// Package todoevents is the provider-neutral to-do list model: an Item,
// its Status, and the Event variants that mutate a list of them.
//
// It holds no provider's wire shape. Each provider's plugin reads its
// own messages and returns an Event from them
// (agent.Provider.ExtractTodoEvent); the worker owns the canonical
// state in agent_todos and broadcasts the post-mutation snapshot to
// clients via AgentTodosChanged. The frontend does not reduce these
// events locally.
package todoevents

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MaxTodos caps the size of an agent's to-do list shipped to clients
// and held in memory by the reducer. Practically, an agent rarely
// produces more than a few dozen rows; the cap is a guardrail against
// a runaway one flooding `agent_todos` and making every cold-start
// payload pathologically large.
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
// StatusDeleted as a single "finished" pool.
type Status int

const (
	StatusPending Status = iota
	StatusInProgress
	StatusCompleted
	StatusDeleted
)

// IsFinished reports whether s is a final status — one that makes a
// row eligible for cap-eviction (Completed | Deleted). Pending and
// InProgress rows are never evicted; they only leave the list through
// an explicit Delete event.
func (s Status) IsFinished() bool {
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
	// KindSnapshot replaces the whole list. It is what a provider that
	// re-sends every row on each change produces, which is most of them.
	KindSnapshot EventKind = iota
	// KindCreate appends one row, or replaces the row that already
	// carries its ID, so a replayed create is idempotent.
	KindCreate
	// KindUpdate merges the Patch into the row identified by ID.
	KindUpdate
	// KindDelete tombstones the row identified by ID.
	KindDelete
	// KindDetail merges a full row into the row identified by ID, and
	// appends it when that ID is unseen. It is what a read-only query
	// of one row produces, so it never downgrades a status; see
	// MergeDetail.
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
// Used by the worker's persistence layer to apply a KindUpdate.
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
// KindDetail carries a full snapshot of one row; a missing field maps
// to an empty string via StatusFromWire or a json zero value, which we
// treat as "preserve". StatusPending is the zero value of Status and
// the default for an empty wire string, so base wins there too — the
// read-only query that produces a KindDetail never legitimately
// downgrades a row from in_progress or completed back to pending. A
// real status transition arrives as a KindUpdate.
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
