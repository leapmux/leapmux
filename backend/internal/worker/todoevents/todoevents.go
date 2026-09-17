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

// Status is the canonical to-do status: a DEFINED type over
// leapmuxv1.TodoStatus, so this package, the agent_todos.status column
// and the browser share one numbering and every conversion is a cast.
// A defined type rather than an alias, because an alias cannot carry
// IsFinished.
//
// The zero value is StatusUnspecified, not a real status. It used to be
// StatusPending; the column's CHECK now refuses 0, so a write that
// never set a status fails rather than recording a pending row.
//
// StatusDeleted is a tombstone: a KindUpdate whose Patch sets it marks
// the row deleted instead of removing it, so the chat thread can keep
// rendering the deletion event and the sidebar can show the deleted row
// with a distinct visual. A tombstone travels on the patch like every
// other status, so one frame that deletes a task and renames it keeps
// both halves. Cap eviction treats StatusCompleted and StatusDeleted as
// a single "finished" pool.
type Status leapmuxv1.TodoStatus

const (
	StatusUnspecified = Status(leapmuxv1.TodoStatus_TODO_STATUS_UNSPECIFIED)
	StatusPending     = Status(leapmuxv1.TodoStatus_TODO_STATUS_PENDING)
	StatusInProgress  = Status(leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS)
	StatusCompleted   = Status(leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED)
	StatusDeleted     = Status(leapmuxv1.TodoStatus_TODO_STATUS_DELETED)
)

// String states the status with the proto enum's own generated name, for logs.
func (s Status) String() string { return leapmuxv1.TodoStatus(s).String() }

// IsFinished reports whether s is a final status — one that makes a
// row eligible for cap-eviction (Completed | Deleted). Pending and
// InProgress rows are never evicted. They leave the list only when a
// KindUpdate patches them to a final status, or when a KindSnapshot
// replaces the whole list.
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
	// KindUpdate merges the Patch into the row identified by ID. It
	// carries a deletion too: a Patch that sets StatusDeleted tombstones
	// the row, and the same frame's text fields land with it.
	KindUpdate
	// KindDetail merges a full row into the row identified by ID, and
	// appends it when that ID is unseen. It is what a read-only query
	// of one row produces, so it never downgrades a status; see
	// MergeDetail.
	KindDetail
	// KindMerge upserts EVERY row in Items and leaves every row it does
	// not list alone. It is KindCreate applied N times, so each listed
	// row REPLACES the row that carries its ID, and a row whose ID is
	// unseen is appended.
	//
	// It is what a provider produces when one message states the rows
	// that changed rather than the whole list. Cursor's
	// `cursor/update_todos` is that provider: it sends the full list
	// once with `merge:false`, and every later frame carries the
	// changed subset with `merge:true`. Reading such a subset as a
	// KindSnapshot would delete every row the frame stayed silent about.
	KindMerge
)

// Event is the discriminated union of mutation variants. Fields are
// populated based on Kind; readers must switch on Kind before reading.
type Event struct {
	Kind     EventKind
	Snapshot []Item // KindSnapshot
	Items    []Item // KindMerge (the rows that changed)
	Item     Item   // KindCreate / KindDetail (full row)
	ID       string // KindUpdate (target id)
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
// to an empty string or a json zero value, which we treat as
// "preserve".
//
// StatusUnspecified is the zero value and means the detail reported no
// status, so base wins there. This is sharper than the rule it
// replaced: while zero meant PENDING, an explicitly reported `pending`
// was indistinguishable from an absent one and both were dropped. A
// real status transition still arrives as a KindUpdate.
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
	if detail.Status != StatusUnspecified {
		out.Status = detail.Status
	}
	return out
}

// ToProto converts an in-memory Item to the wire-format proto message.
func (i Item) ToProto() *leapmuxv1.TodoItem {
	return &leapmuxv1.TodoItem{
		Id:          i.ID,
		Content:     i.Content,
		Status:      leapmuxv1.TodoStatus(i.Status),
		ActiveForm:  i.ActiveForm,
		Description: i.Description,
	}
}

// StatusFromProviderWord parses a PROVIDER's own status word onto the neutral
// status. Every provider that reports to-dos spells these four states in
// roughly the same lowercase words, so one parser serves all of them and no
// plugin carries a copy.
//
// It is not a storage or payload codec, and it has no inverse: agent_todos
// stores the ordinal, and clients read the proto enum. It reads only inward,
// from a vocabulary this project does not own.
//
// The EMPTY word and an unrecognized one are different answers, and keeping
// them apart is what lets MergeDetail preserve a row.
//
//   - "" means the provider's payload carried no status at all, which is
//     StatusUnspecified. A KindDetail built from it must leave the row's status
//     alone -- Claude's TaskGet omits the field on a task whose status did not
//     change, and reading that as `pending` silently downgraded a row that was
//     in progress.
//   - Any other unrecognized word is a real report this parser does not know,
//     so it reads as StatusPending, the state that claims the least about it.
//
// StatusUnspecified is an in-flight value only: the column refuses it, and
// OrPending resolves it wherever an Item becomes a row.
func StatusFromProviderWord(word string) Status {
	switch word {
	case "in_progress", "inProgress":
		return StatusInProgress
	case "completed":
		return StatusCompleted
	case "deleted", "cancelled", "canceled":
		// A provider that CANCELS a task reports the same end state that
		// StatusDeleted tombstones: the row stays visible and stops being work.
		// Cursor and OpenCode both spell it `cancelled`, so it is shared rather than
		// one provider's word; the American spelling is here because the word travels
		// as prose and no protocol fixes it.
		return StatusDeleted
	case "":
		return StatusUnspecified
	default:
		return StatusPending
	}
}

// OrPending resolves s for STORAGE: a persisted row always carries a real
// state, so a status no provider reported becomes StatusPending.
//
// It runs at the write, never at the parse, and the order matters. MergeDetail
// reads StatusUnspecified as "keep the row's own status", so resolving earlier
// would turn every silent detail into a downgrade to pending -- which is the
// regression TestMergeDetail_PreservesStatusWhenTheDetailReportedNone covers.
// The agent_todos CHECK is the backstop for a path that skips this.
func (s Status) OrPending() Status {
	if s == StatusUnspecified {
		return StatusPending
	}
	return s
}

// ItemsToProto bulk-converts a slice for proto-shaped responses.
func ItemsToProto(items []Item) []*leapmuxv1.TodoItem {
	out := make([]*leapmuxv1.TodoItem, len(items))
	for i, it := range items {
		out[i] = it.ToProto()
	}
	return out
}
