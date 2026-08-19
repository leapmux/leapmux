// Package bgtask holds the provider-neutral model for the background-task
// registry (the "Background tasks" sidebar section). Providers feed rows to the
// worker OutputSink via neutral Upsert/Close primitives (see OutputSink);
// this package owns the in-memory model, the proto / DB-column conversions,
// and the neutral normalization rules that every provider's write meets
// (ValidateRowKey, Upsert.CleanTitle, Item.PreservingBlanksFrom). Each rule
// lives here so the registry applier and the provider tests' recording sink
// share one copy and cannot drift.
// There is no reducer here -- unlike agent_todos, the provider
// integrations translate their native events directly into sink calls, so the
// shared package stays free of provider-specific names and shapes.
//
// One registry per ROOT main agent (owner_agent_id); rows for descendants at
// any depth live under the root.
package bgtask

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/validate"
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

// RowKeyByteLimit caps a provider-supplied registry row key.
//
// A row key is not a name, so NameByteLimit is the wrong number for it: it is
// an opaque provider identifier, and a UUID with a prefix already runs past
// 64 bytes. The cap exists because the PROVIDER chooses the length and
// nothing else on the path does: the key is a primary key column, it is held
// in memory for the life of the agent, and every registry snapshot broadcast
// carries it again. MaxTasks caps how MANY rows exist and says nothing about
// how large one is.
const RowKeyByteLimit = 256

// ValidateRowKey reports why a provider-supplied registry row key is unusable,
// or nil when the key is usable. The empty key is accepted and means "this
// call carries no registry linkage" -- EnsureChildAgent and
// RenameBackgroundTask each test for it and do less work.
//
// THIS RULE REFUSES AND NEVER REWRITES, and that is the whole point of it. A
// row key is an IDENTITY: it is the second half of the
// (owner_agent_id, row_key) primary key, and every later upsert, status
// change, close and rename addresses the row by it. A rule that rewrites an
// identifier is not injective, so two provider keys can map onto one string --
// and then two background tasks share one registry row, the second overwrites
// the first's title and status, and one of the two disappears from the
// sidebar. validate.ValidateSessionID refuses rather than strips for the same
// reason, and the earlier version of this function stated the invariant ("two
// keys that differ only in their whitespace must stay two keys") while a strip
// and a cap broke it.
//
// It refuses exactly what makes a key UNUSABLE, and nothing that makes it
// merely hard to read:
//
//   - Over RowKeyByteLimit bytes. The PROVIDER chooses the length and nothing
//     else on the path limits it: the key is a primary-key column, it stays in
//     memory for the life of the agent, and every registry snapshot broadcast
//     carries it again. Refusing keeps that bound AND injectivity, which a cap
//     cannot do -- no total function onto a bounded set is injective.
//   - Invalid UTF-8. BackgroundTaskItem.id is a proto `string`, and a marshal
//     of an invalid one fails the WHOLE registry broadcast, not this row alone.
//
// A control character, a C1 byte, a bidirectional override and an embedded
// newline are NOT refused. At least one provider (Cursor) ships toolCallIds
// with an embedded newline, so refusing them would drop that provider's rows
// altogether. They are a READABILITY problem, and they are answered where the
// key is READ: the row key is the last fallback of a registry row's label, and
// `rowTitle` in frontend/src/components/backgroundtasks/BackgroundTaskList.tsx
// cleans it there. A log line needs no help, because slog quotes a value that
// holds a control character.
func ValidateRowKey(s string) error {
	if len(s) > RowKeyByteLimit {
		return fmt.Errorf("registry row key must be at most %d bytes (got %d)", RowKeyByteLimit, len(s))
	}
	if !utf8.ValidString(s) {
		return errors.New("registry row key must be valid UTF-8")
	}
	return nil
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

// CleanTitleRunes cleans s with the shared title rule and then cuts it to at
// most max runes. Call it instead of TruncateRunes wherever the cut result
// becomes a title.
//
// The order is CLEAN FIRST, CUT SECOND, for the reason validate.CleanName
// gives: a cut that runs first spends its budget on characters the clean is
// about to remove. A spawn prompt whose first line opens with `max`
// zero-width characters lost its whole title that way -- the cut kept the
// invisible run, the clean then emptied it, and the row kept the title it
// already had.
//
// The trailing trim is required because the rune cut can land on the one
// space that separated two words, exactly as it is inside CleanName.
//
// The result is already what CleanName returns for it, so the sink's own
// CleanName is a no-op on it: the rule is idempotent, and max runes of a
// value that is at most NameByteLimit bytes stays at most NameByteLimit
// bytes.
func CleanTitleRunes(s string, max int) string {
	return strings.TrimSpace(TruncateRunes(validate.CleanName(s), max))
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

// CleanTitle returns a copy of u whose Title meets the rule that every title
// column in the worker shares: validate.CleanName, whose doc holds the steps.
// The registry applier calls it, and so does the recording sink the provider
// tests assert against, so the two cannot disagree about what a provider's
// title becomes.
//
// A row that sets TitleIsCommand keeps its command whole, quoting included.
// The rule used to strip `"`, `\`, `$` and `%`, so `npm test --grep "$FOO"`
// reached the row as `npm test --grep FOO` and the label identified a command
// that nobody ran. Nothing depended on that strip: the command executes from
// the provider's own field, never from this title.
//
// A Title that the rule empties says what a blank Title says: this upsert
// carries no usable title. So it takes the same answer -- the row keeps the
// title it already holds (see PreservingBlanksFrom), and a new row is born
// untitled. A placeholder written here would overwrite a real title with a
// name the model never wrote. A provider that has a better fallback applies it
// BEFORE it calls the sink, the way acpBridge's terminal/create falls back to
// "shell".
//
// TitleIsCommand travels with the title it describes, so an emptied title
// clears it. PreservingBlanksFrom restores the stored pair on the update path;
// on the insert path there is no pair to restore, and a blank title set as
// code is a blank in the monospace face.
//
// validate.CleanName is idempotent, so a caller that cleaned the title already
// (EnsureChildAgent does, for the `agents` row it writes first) passes it
// through byte-identical.
func (u Upsert) CleanTitle() Upsert {
	u.Title = validate.CleanName(u.Title)
	if u.Title == "" {
		u.TitleIsCommand = false
	}
	return u
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
