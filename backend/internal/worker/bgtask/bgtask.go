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
	"crypto/sha256"
	"encoding/hex"
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

// LabelByteLimit caps every provider-chosen LABEL field on a registry row.
//
// The argument RowKeyByteLimit makes applies verbatim to `ActiveForm`,
// `Description` and `GroupLabel`: the PROVIDER chooses the length and nothing
// else on the path does. They are model-written text, they are held in memory
// for the life of the agent, and every registry snapshot broadcast carries them
// again. MaxTasks caps how MANY rows exist and says nothing about how large one
// is, and the sqlite column has no limit of its own.
//
// 512 rather than NameByteLimit's 128: `Description` carries a file path, which
// a 128-byte cut would mangle. It matches notificationBodyByteLimit, which
// bounds provider-chosen prose for the same reason.
//
// `GroupKey` and `RowKey` are NOT capped here. They are identities the registry
// joins on, and a cut is non-injective -- which is the whole reason
// ValidateRowKey refuses an unusable key rather than rewriting it.
const LabelByteLimit = 512

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
//     carries it again. Refusing keeps that bound AND injectivity, which a CUT
//     cannot do -- no total function onto a bounded set is injective, and a cut
//     is the worst of them, because provider keys share a prefix by
//     construction. NormalizeRowKey is what turns this refusal back into a row.
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
	return validateIdentity("registry row key", s)
}

// derivedRowKeyPrefix marks a row key this package derived because the
// provider's own key was unusable. It is not a namespace a provider can reach:
// a provider that sent this exact prefix followed by 64 hex characters would
// have to be sending the sha256 of some other string it never sent.
const derivedRowKeyPrefix = "leapmux-derived-key:"

// NormalizeRowKey returns the row key to store for the provider-supplied key s:
// s itself when ValidateRowKey accepts it, and a key derived from it when it
// does not.
//
// TOTAL, and that is the point. ValidateRowKey states exactly what makes a key
// unusable, and every caller answered a refusal by failing the write -- so a
// provider that ships one over-long key loses the whole row, and the background
// task never appears in the sidebar at all. The user sees a subagent that is
// running with nothing on screen to say so, which is a worse answer than an
// unreadable label.
//
// A DERIVED KEY IS STILL AN IDENTITY, which is what lets it replace the
// refusal. The row key is the second half of the (owner_agent_id, row_key)
// primary key, and every later upsert, status change, close and rename
// addresses the row by it, so the rule that produces it must be a function of
// the provider's key alone and must not map two keys onto one:
//
//   - A function of s alone. The same provider key normalizes to the same
//     derived key on every call, in every process, so the upsert that opens the
//     row and the close that finishes it land on the same row across a worker
//     restart.
//   - Injective in practice. A CUT is not: providers build keys by prefixing a
//     fixed namespace to a payload, so two keys that pass RowKeyByteLimit share
//     their first 256 bytes far more readily than at random. Two keys collide
//     here only through a sha256 collision.
//   - Bounded and valid UTF-8 by construction, so the derived key satisfies
//     ValidateRowKey itself. Normalizing an already-derived key returns it
//     unchanged, so a value that passes through twice is stable.
//
// It answers the invalid-UTF-8 refusal as well as the over-long one, which is
// the case wireString cannot help with: `Id` is the one proto string on a
// registry row that is an identity rather than prose, so it cannot be repaired
// on the projection without moving the row.
//
// The caller decides whether to say anything. `key != s` is the whole test, and
// ValidateRowKey supplies the reason for the log line.
func NormalizeRowKey(s string) string {
	if ValidateRowKey(s) == nil {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return derivedRowKeyPrefix + hex.EncodeToString(sum[:])
}

// ValidateGroupKey reports why a provider-supplied GROUP key is unusable, or
// nil when it is usable. The empty key is accepted and means "this row is not
// grouped".
//
// The same class as a row key, because the two are the same KIND of value: an
// identity the registry joins on, chosen by the provider, with nothing else on
// the path bounding it. `LabelByteLimit`'s own doc names them together and says
// why neither may be cut; the refusal was then built for one of the two.
//
// What a caller DOES with the refusal differs, and that difference is the whole
// point. A refused row key fails the write, because a row with no identity
// cannot exist. A refused group key clears the grouping in `Upsert.Clean` and
// the row stands ungrouped.
func ValidateGroupKey(s string) error {
	return validateIdentity("registry group key", s)
}

// validateIdentity is the rule both keys share, so the cap-versus-refuse
// decision is made once for the class rather than per field.
func validateIdentity(what, s string) error {
	if len(s) > RowKeyByteLimit {
		return fmt.Errorf("%s must be at most %d bytes (got %d)", what, RowKeyByteLimit, len(s))
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s must be valid UTF-8", what)
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

// truncateRunes truncates s to at most max runes. A byte slice at max can land
// mid-rune and emit invalid UTF-8, which fails the proto broadcast marshal;
// counting runes avoids that. max <= 0 returns s unchanged.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// CleanTitleRunes cleans s with the shared title rule and then cuts it to at
// most max runes.
//
// It is the only cut this package exports. A cut WITHOUT the clean is the
// shortcut the next provider reaches for, and it re-introduces the untrimmed
// title this rule exists to remove -- so `truncateRunes` stays unexported and
// this function is the door.
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
	return strings.TrimSpace(truncateRunes(validate.CleanName(s), max))
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
// a group), and a blank incoming LABEL under the SAME key keeps the existing
// label. Status is NOT preserved: callers set it deliberately, and
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
	} else if i.GroupLabel == "" && i.GroupKey == existing.GroupKey {
		// The label is a descriptive field like every other one above, so a
		// partial upsert that omits it must not erase it. The key alone did not
		// cover this: a provider that sends the group key on every update but
		// the workflow name only on the first one blanked the heading from the
		// second update onward, and `Upsert.Clean` reaches the same state by
		// cleaning a label of nothing but invisible characters to "".
		//
		// GUARDED BY THE KEY, because the label names THAT group. Restoring it
		// under a different key would put the previous group's heading over a
		// row that just moved to a new group, and a wrong heading is worse than
		// a missing one -- the reader cannot tell it is wrong.
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
//
// The three sibling LABEL fields are all capped at LabelByteLimit here, and TWO
// of them keep their whitespace with StripUnreadable: `Description` carries a
// file path and `ActiveForm` carries prose whose line structure is the
// provider's. StripUnreadable answers that -- it asks the whitespace question
// before the control question, so a line break survives instead of gluing the
// words on either side of it together.
//
// `GroupLabel` is the third, and it takes the TITLE rule instead, because it is
// the same kind of value: one line, read as a group heading, with no structure
// of its own to keep. Sharing its siblings' rule cost it the blank test that
// every descriptive field depends on -- a heading of nothing but invisible
// characters stripped to a run of SPACES, which is not "" and so slipped past
// PreservingBlanksFrom, and the group then drew a heading with nothing in it
// over a stored name that was still good. The reader cleaned it to nothing
// again on the way to the screen, so the two ends already disagreed about what
// the value was.
//
// `GroupKey` is never CUT, for the reason ValidateRowKey never cuts a row key:
// it is an identity, and a cut is non-injective, so two distinct groups would
// collapse into one heading. An unusable one is DROPPED instead, together with
// the label that names it -- the row then stands ungrouped, which is a state
// the registry already has (`GroupKey == ""` means "no grouping", and
// PreservingBlanksFrom reads it that way).
//
// Dropping the GROUPING rather than the ROW is the difference from RowKey. A
// row with no usable identity cannot exist; a row whose GROUP identity is
// unusable is still a perfectly good row, and refusing it would lose a task the
// user is waiting on over the heading it would have sat under.
func (u Upsert) Clean() Upsert {
	u.Title = validate.CleanName(u.Title)
	if u.Title == "" {
		u.TitleIsCommand = false
	}
	if ValidateGroupKey(u.GroupKey) != nil {
		u.GroupKey = ""
		u.GroupLabel = ""
	}
	u.ActiveForm = validate.StripUnreadable(u.ActiveForm, LabelByteLimit)
	u.Description = validate.StripUnreadable(u.Description, LabelByteLimit)
	u.GroupLabel = validate.CleanNameTo(u.GroupLabel, LabelByteLimit)
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

// wireString returns s with every invalid UTF-8 byte removed.
//
// A proto `string` must be valid UTF-8, and `proto.Marshal` fails the WHOLE
// message for ONE bad byte -- so a single provider field would drop EVERY
// background task from the broadcast, from the cold-start RPC response, and
// from every later snapshot. `broadcastBackgroundTasks` puts the entire
// registry in one message, and sqlite stores the bad bytes verbatim, so the
// worker's own seed reads them back and re-poisons the cache on every boot: the
// sidebar for that agent stays empty until the row is evicted, and eviction
// only ever takes finished rows.
//
// An IDENTITY is kept valid at the boundary instead, because a rewrite here
// would move it: `NormalizeRowKey` derives a usable `id`, and `Upsert.Clean`
// clears an unusable group key. A label cannot be refused without losing the
// row, so it is repaired HERE, on the projection: the cached and stored value
// stays exactly what the provider sent, and the repair is derived from it on
// every read.
//
// It drops the byte rather than writing U+FFFD, the same answer
// `validate.CleanNameChars` gives an invalid byte, so the two rules agree.
func wireString(s string) string {
	return strings.ToValidUTF8(s, "")
}

// ToProto converts an in-memory Item to the wire-format proto message.
func (i Item) ToProto() *leapmuxv1.BackgroundTaskItem {
	// THE GROUPING IS DROPPED, NEVER REPAIRED. `GroupKey` used to go through
	// wireString, and that was the one place this file rewrote a join identity:
	// the client groups rows into one heading by the wire key, so dropping a
	// byte from it merged two groups on screen while the worker's own cache kept
	// them apart -- a disagreement with no error anywhere to find it by.
	//
	// Shipping it unrepaired is not the alternative, because ONE invalid byte in
	// any proto string fails the marshal of the whole registry message, which is
	// the failure the rest of this function exists to prevent. So an unusable
	// key costs the GROUPING and nothing else: the row ships ungrouped rather
	// than mis-grouped, which is the answer `Upsert.Clean` already gives at the
	// write boundary. Stating it here too makes it unreachable rather than
	// merely unreached -- `Clean` guards every write path today, and an Item
	// that never met it would otherwise empty the sidebar.
	//
	// The LABEL goes with the key, for the reason `Upsert.Clean` pairs them:
	// a heading that names no group has nothing left to name.
	groupKey, groupLabel := i.GroupKey, i.GroupLabel
	if ValidateGroupKey(groupKey) != nil {
		groupKey, groupLabel = "", ""
	}
	return &leapmuxv1.BackgroundTaskItem{
		// NO IDENTITY IS REPAIRED HERE, and each one is kept valid by a rule of
		// its own: `Id` by `NormalizeRowKey` at the sink boundary,
		// `ChildAgentId` and `ParentAgentId` by `id.Generate`, and `GroupKey` by
		// the guard above. `GroupLabel` keeps the wireString repair because it
		// is prose: nothing joins on it.
		Id:             i.RowKey,
		Kind:           kindToProto(i.Kind),
		ChildAgentId:   i.ChildAgentID,
		ParentAgentId:  i.ParentAgentID,
		GroupKey:       groupKey,
		GroupLabel:     wireString(groupLabel),
		Title:          wireString(i.Title),
		TitleIsCommand: i.TitleIsCommand,
		Description:    wireString(i.Description),
		ActiveForm:     wireString(i.ActiveForm),
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
