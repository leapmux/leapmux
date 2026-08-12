// Package bgtask holds the provider-neutral model for the background-task
// registry (the "Background tasks" sidebar section). Providers feed rows to the
// worker OutputSink via neutral Upsert/Close primitives (see OutputSink);
// this package owns ONLY the in-memory model and the proto / DB-column
// conversions. There is no reducer here -- unlike agent_todos, the provider
// integrations translate their native events directly into sink calls, so the
// shared package stays free of provider-specific names and shapes.
//
// One registry per ROOT main agent (owner_agent_id); rows for descendants at
// any depth live under the root.
package bgtask

import (
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MaxTasks caps how many rows OF EACH KIND an agent's background-task registry
// ships to clients and holds in memory. Finished rows are evicted past this cap
// (oldest first); active rows are never evicted. Matches the agent_todos
// guardrail.
//
// PER KIND, not per registry: a run that opens hundreds of shells would
// otherwise evict every finished subagent, and the subagent rows are the ones
// that carry a transcript worth revisiting. Each kind gets its own pool, so a
// burst of one cannot push out the other.
const MaxTasks = 64

// SanitizeRowKey strips control characters (bytes < 0x20) from a
// provider-supplied registry row key. Row keys reach DOM data-attributes and
// log lines; at least one provider (Cursor) ships toolCallIds with an embedded
// newline. Applied in the neutral layer (the sink entry points) so every
// provider is covered without a per-integration fix.
func SanitizeRowKey(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}

// FirstLine returns the content up to the first newline (trimmed). Empty input
// yields "". Used by provider integrations to derive a concise registry title
// from a multi-line spawn prompt. Lives in this neutral package because every
// provider (Claude, Codex, Pi) needs it and it carries no provider semantics.
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// TruncateRunes truncates s to at most max runes. A byte slice at max can land
// mid-rune and emit invalid UTF-8, which fails the proto broadcast marshal;
// counting runes avoids that. max <= 0 returns s unchanged.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// Kind discriminates subagent rows (an openable transcript tab) from shell
// rows (a background process with no transcript).
type Kind int

const (
	KindUnspecified Kind = iota
	KindSubagent
	KindShell
)

// Kinds lists every real (non-unspecified) kind, in declaration order. It is the
// single source of truth for the registry's cap pools: the cold-start seed loads
// each one to its own cap, so adding a kind here gives it a pool automatically.
//
// Add a new kind to BOTH this array and KindWire. KindWire's default arm maps an
// unrecognized kind onto "subagent", which is the safe answer for a
// KindUnspecified row but the WRONG pool for a real new kind, so the two must
// stay in step. KindWires() below is what the seed reads, so a kind missing from
// KindWire seeds the wrong pool rather than none.
var Kinds = [...]Kind{KindSubagent, KindShell}

// KindWires returns every pool's wire name, in Kinds order. The registry seeds
// and caps by this list.
func KindWires() []string {
	out := make([]string, len(Kinds))
	for i, k := range Kinds {
		out[i] = KindWire(k)
	}
	return out
}

// Status is the canonical background-task status. Zero == pending (friendlier
// than the proto's UNSPECIFIED). Interrupted is set at worker boot for tasks
// the previous process left active (an honest "worker restarted" label).
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusCompleted
	StatusFailed
	StatusStopped
	StatusInterrupted
)

// IsFinished reports whether s is a final status -- one that makes a row
// eligible for cap-eviction. Pending and Running rows are never evicted; they
// only leave through a transition into a final status.
func (s Status) IsFinished() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusStopped || s == StatusInterrupted
}

// Item mirrors leapmuxv1.BackgroundTaskItem in a plain-Go shape so the
// registry pipeline stays decoupled from the proto generated types. A zero
// EndedAt means the task is still active.
type Item struct {
	RowKey        string
	ChildAgentID  string
	ParentAgentID string
	Kind          Kind
	GroupKey      string
	GroupLabel    string
	Title         string
	// TitleIsCommand marks Title as a verbatim shell command rather than prose,
	// so a client can set it as code. Only a provider that hands the command
	// over ITSELF may set it: an ACP terminal/create carries the command and
	// nothing else, while Claude's task_started carries `description ||
	// command`, so a Claude shell row's title is prose whenever the model wrote
	// one and nothing on the wire says which it was.
	TitleIsCommand bool
	Description    string
	ActiveForm     string
	Status         Status
	CreatedAt      time.Time
	UpdatedAt      time.Time
	EndedAt        time.Time // zero == active
}

// PreservingBlanksFrom returns a copy of i with each descriptive field that the
// caller left blank (empty string, or KindUnspecified) filled from existing.
// GroupKey and GroupLabel are preserved together as a pair: a blank incoming
// GroupKey keeps BOTH the existing key and label (a partial upsert cannot clear
// a group). Status is NOT preserved: callers set it deliberately, and
// StatusPending is a valid value, not a sentinel for "keep". Centralizes the
// "blank means keep" rule so adding a descriptive field cannot silently regress
// a partial upsert.
func (i Item) PreservingBlanksFrom(existing Item) Item {
	if i.ChildAgentID == "" {
		i.ChildAgentID = existing.ChildAgentID
	}
	if i.ParentAgentID == "" {
		i.ParentAgentID = existing.ParentAgentID
	}
	if i.Kind == KindUnspecified {
		i.Kind = existing.Kind
	}
	if i.GroupKey == "" {
		i.GroupKey = existing.GroupKey
		i.GroupLabel = existing.GroupLabel
	}
	if i.Title == "" {
		i.Title = existing.Title
		// The flag describes THIS title, so it travels with it. Preserving one
		// without the other would label the kept title by the blank upsert's
		// answer, and set a shell command in prose type (or the reverse).
		i.TitleIsCommand = existing.TitleIsCommand
	}
	if i.Description == "" {
		i.Description = existing.Description
	}
	if i.ActiveForm == "" {
		i.ActiveForm = existing.ActiveForm
	}
	return i
}

// Upsert is the neutral write shape providers hand to
// OutputSink.UpsertBackgroundTask. It never carries seq/created_at (those are
// allocated by the sink on insert) nor ended_at (set on close).
type Upsert struct {
	RowKey        string
	Kind          Kind
	ChildAgentID  string // "" for registry-only rows
	ParentAgentID string // immediate parent agent id; "" == the owner itself
	GroupKey      string
	GroupLabel    string
	Title         string
	// See Item.TitleIsCommand. Set it only when the provider handed over the
	// command itself; a provider that cannot tell prose from a command leaves
	// it false, because prose in the monospace face reads worse than a command
	// in the normal one.
	TitleIsCommand bool
	Description    string
	ActiveForm     string
	Status         Status
}

// ToItem projects an Upsert onto the Item it describes, leaving the fields the
// SINK owns (seq, created_at, updated_at, ended_at) at their zero values for
// the caller to stamp.
//
// One projection, so the registry applier and the provider tests' recording
// sink cannot disagree about what an upsert carries. They each hand-copied the
// fields before, which meant a new field on Upsert reached production and NOT
// the fake -- and the provider tests, which assert against the fake, went on
// passing while the field they were meant to cover never arrived.
func (u Upsert) ToItem() Item {
	return Item{
		RowKey:         u.RowKey,
		ChildAgentID:   u.ChildAgentID,
		ParentAgentID:  u.ParentAgentID,
		Kind:           u.Kind,
		GroupKey:       u.GroupKey,
		GroupLabel:     u.GroupLabel,
		Title:          u.Title,
		TitleIsCommand: u.TitleIsCommand,
		Description:    u.Description,
		ActiveForm:     u.ActiveForm,
		Status:         u.Status,
	}
}

// ToProto converts an in-memory Item to the wire-format proto message.
func (i Item) ToProto() *leapmuxv1.BackgroundTaskItem {
	return &leapmuxv1.BackgroundTaskItem{
		Id:             i.RowKey,
		Kind:           kindToProto(i.Kind),
		ChildAgentId:   i.ChildAgentID,
		ParentAgentId:  i.ParentAgentID,
		GroupKey:       i.GroupKey,
		GroupLabel:     i.GroupLabel,
		Title:          i.Title,
		TitleIsCommand: i.TitleIsCommand,
		Description:    i.Description,
		ActiveForm:     i.ActiveForm,
		Status:         statusToProto(i.Status),
		CreatedAt:      formatTime(i.CreatedAt),
		UpdatedAt:      formatTime(i.UpdatedAt),
		EndedAt:        formatTime(i.EndedAt),
	}
}

// WithUpdatedAt returns a copy of i with the given UpdatedAt. It exists so the
// upsert no-op guard can compare an existing row against a merged candidate
// WITHOUT the candidate's freshly-stamped UpdatedAt -- which is `now` on every
// call and would otherwise make the equality check almost never true, leaking
// every byte-identical replay into a DB write + broadcast.
func (i Item) WithUpdatedAt(t time.Time) Item {
	i.UpdatedAt = t
	return i
}

// ItemsToProto bulk-converts a slice for proto-shaped responses and broadcasts.
func ItemsToProto(items []Item) []*leapmuxv1.BackgroundTaskItem {
	out := make([]*leapmuxv1.BackgroundTaskItem, len(items))
	for i, it := range items {
		out[i] = it.ToProto()
	}
	return out
}

// KindWire returns the lowercase DB-column string used by
// agent_background_tasks.kind ("subagent" | "shell").
func KindWire(k Kind) string {
	switch k {
	case KindShell:
		return "shell"
	default:
		return "subagent"
	}
}

// KindFromWire parses the DB-column string; unknown values fall through to
// KindSubagent.
func KindFromWire(s string) Kind {
	switch s {
	case "shell":
		return KindShell
	default:
		return KindSubagent
	}
}

// StatusWire returns the lowercase DB-column string used by
// agent_background_tasks.status.
func StatusWire(s Status) string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusStopped:
		return "stopped"
	case StatusInterrupted:
		return "interrupted"
	default:
		return "pending"
	}
}

// StatusFromWire parses the DB-column string; unknown values fall through to
// StatusPending.
func StatusFromWire(s string) Status {
	switch s {
	case "running":
		return StatusRunning
	case "completed":
		return StatusCompleted
	case "failed":
		return StatusFailed
	case "stopped":
		return StatusStopped
	case "interrupted":
		return StatusInterrupted
	default:
		return StatusPending
	}
}

func kindToProto(k Kind) leapmuxv1.BackgroundTaskKind {
	switch k {
	case KindShell:
		return leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL
	default:
		return leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT
	}
}

func statusToProto(s Status) leapmuxv1.BackgroundTaskStatus {
	switch s {
	case StatusRunning:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING
	case StatusCompleted:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED
	case StatusFailed:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED
	case StatusStopped:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED
	case StatusInterrupted:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_INTERRUPTED
	default:
		return leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_PENDING
	}
}

// formatTime renders time.Time into the proto's canonical string layout; a
// zero time renders as "" (the proto's "empty == active/absent" convention).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
