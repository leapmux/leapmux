package inputqueue

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	// MaxItems, MaxItemBytes, and MaxAttachmentsPerItem come from
	// contracts/agent-input.json, because the browser pre-checks a composer
	// draft against the same numbers before it sends EnqueueAgentInput.
	MaxItems                  = contracts.MaxAgentInputItems
	MaxItemBytes              = contracts.MaxAgentInputItemBytes
	MaxQueueAttachmentBytes   = 100 * 1024 * 1024
	maxQueueIdentityByteCount = 256
	// MaxAttachmentsPerItem limits metadata growth in authoritative snapshots.
	MaxAttachmentsPerItem = contracts.MaxAgentInputAttachmentsPerItem
	// MaxAttachmentFilenameBytes accepts names beyond common filesystem limits
	// while it keeps a maximum-size queue snapshot inside the wire budget.
	MaxAttachmentFilenameBytes = 1024
	// MaxAttachmentMIMETypeBytes limits untrusted media type metadata.
	MaxAttachmentMIMETypeBytes = 256
	// snapshotTextPreviewCharacters caps the text that a snapshot carries for
	// one item. The store applies it in SQL, so a 10 MiB item never enters
	// memory; the service applies the derived byte cap below at the wire
	// boundary. One limit, two layers, because the two costs differ.
	snapshotTextPreviewCharacters = 1024
	// SnapshotTextPreviewBytes is the same limit measured the way the wire
	// measures it. A UTF-8 rune takes at most 4 bytes, so this is what the SQL
	// character cap can produce at its widest.
	SnapshotTextPreviewBytes = snapshotTextPreviewCharacters * 4
)

// pauseOwner records which cause created the pause that still holds, so only
// that cause lifts it again: an archive resume, or the end of a planned
// restart, then leaves a pause that a later crash or the user created. A new
// pause records its cause as the owner. pauseAndEndTurn keeps an existing
// pause's owner, except that it takes over a pause a planned restart owns.
// Real values persist in agent_input_queue_state.pause_owner. The zero value
// means absence in memory and maps to SQL NULL.
//
// A DEFINED type over the proto enum, not an independent iota. The column is a
// closed set, so its numbering comes from proto like every other enum column
// here -- the Go constant, the column and the SQL CHECK then share one numbering
// that a renumber moves together. TestEnumColumnChecksMatchTheirProtoRanges pins
// the range. An iota let the three drift with nothing to notice.
type pauseOwner leapmuxv1.AgentInputQueuePauseOwner

const (
	pauseOwnerNone           = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_UNSPECIFIED)
	pauseOwnerManual         = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_MANUAL)
	pauseOwnerArchive        = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_ARCHIVE)
	pauseOwnerPlannedRestart = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_PLANNED_RESTART)
	pauseOwnerDelivery       = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_DELIVERY)
	pauseOwnerAgentStopped   = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_AGENT_STOPPED)
	pauseOwnerRecovery       = pauseOwner(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_RECOVERY)
)

var (
	ErrNotFound              = errors.New("queued agent input not found")
	ErrConflict              = errors.New("queued agent input conflicts with existing data")
	ErrEditOwned             = errors.New("queued agent input is edited by another client")
	ErrEditOwnerMismatch     = errors.New("queued agent input edit owner does not match")
	ErrVersionConflict       = errors.New("queued agent input version conflict")
	ErrQueueFull             = errors.New("agent input queue holds 100 items")
	ErrItemTooLarge          = fmt.Errorf("queued agent input exceeds %d MiB", MaxItemBytes/1024/1024)
	ErrQueueAttachmentsLarge = errors.New("agent input queue attachments exceed 100 MiB")
	ErrInvalidInput          = errors.New("invalid queued agent input")
	ErrNotHead               = errors.New("only the queue head supports this operation")
	ErrRetryState            = errors.New("queue head is not failed or delivery uncertain")
	ErrUncertainConfirmation = errors.New("retrying delivery-uncertain input requires confirmation")
	ErrTurnEnded             = errors.New("active turn ended before steering")
	ErrSteeringState         = errors.New("queue state does not permit steering")
	ErrSteeringUnsupported   = errors.New("agent provider does not support steering")
	// ErrScheduled refuses to steer a head whose scheduled instant has not
	// arrived. It must not reuse ErrTurnEnded: the turn still runs.
	ErrScheduled = errors.New("queued agent input waits for its scheduled time")
	// ErrScheduleOrder refuses a move across reorder groups. An immediate item
	// and a future one never share a segment, and two future items share one
	// only at exactly the same scheduled instant.
	ErrScheduleOrder = errors.New("queued agent input cannot move across reorder groups")
	// ErrPreemptionUnsupported is steering's counterpart for the interrupt-only
	// route: the running provider cannot steer and Preempt refuses outright.
	ErrPreemptionUnsupported = errors.New("agent provider does not support preemption")
	// ErrPreemptionState is ErrSteeringState's counterpart. It must not reuse
	// ErrTurnEnded, whose text states STEERING and asserts that a turn ended: the
	// head can also be refused because another tab holds its edit, and the reader
	// saw "active turn ended before steering" for a request that neither steered
	// nor followed a turn that ended.
	ErrPreemptionState = errors.New("queue state does not permit preemption")
	ErrManagerStopped  = errors.New("agent input queue is stopped")
	// ErrAgentMessageEdit refuses an edit of an agent message. Its text is what
	// the sending agent wrote, and the recipient reads it under that agent's
	// name, so an edit would put the user's words in the agent's mouth. The
	// user deletes the message and sends one of their own instead. A browser
	// shows this text as the reason that a queue row offers no edit (see
	// editRefusal), and an agent can send a message to itself, so the text
	// claims no other agent.
	ErrAgentMessageEdit = errors.New("an agent wrote this message, so it cannot be edited; delete it and send your own")
	// ErrPlannedRestart refuses an explicit dispatch while the Worker replaces
	// the agent process. The durable pause_owner records the restart, so the
	// refusal survives a Worker restart that leaves the marker behind.
	ErrPlannedRestart = errors.New("agent input queue waits for a planned agent restart")
)

// DispatchOutcome states what a refused dispatch means for the item and for the
// queue. It replaces a set of sentinel errors, so the decode site is one
// exhaustive switch that the linter checks rather than a chain of errors.Is
// whose ORDER decided the answer.
type DispatchOutcome int

const (
	// DispatchFailed: the provider took the input and something went wrong, or
	// it refused for a reason nothing will resolve. The item is marked FAILED
	// and the user retries it explicitly.
	DispatchFailed DispatchOutcome = iota
	// DispatchUncertain: the Worker cannot tell whether the input arrived.
	// Redispatch could duplicate it, so the user confirms.
	DispatchUncertain
	// DispatchBusy: the agent is inside a turn of its own. Nothing failed and
	// nothing has to change for the item to go out, so the item returns to the
	// queue and the queue stays OPEN. The turn it collided with holds it, and
	// the end of that turn drains it.
	DispatchBusy
	// DispatchNotReady: the input never reached the provider AND the agent must
	// change state before it can. The item returns to the queue and the queue
	// PAUSES: a message the user typed must not need a manual retry because the
	// agent was between processes.
	DispatchNotReady
	// DispatchPending waits for an earlier input's native admission. No turn
	// runs, and a dependency notification asks the queue to dispatch again.
	DispatchPending
)

// String gives the text that a refusal carries into the item's error column,
// which the browser renders. DispatchFailed adds nothing, because there the
// cause already says what happened.
func (o DispatchOutcome) String() string {
	switch o {
	case DispatchPending:
		return "agent input waits for native admission"
	case DispatchBusy:
		return "agent input waits for the turn in flight"
	case DispatchNotReady:
		return "agent cannot accept input yet"
	case DispatchUncertain:
		return "agent input delivery is uncertain"
	case DispatchFailed:
		return ""
	}
	return ""
}

type Attachment struct {
	Filename string
	MimeType string
	Data     []byte
}

// AgentSender is the agent tab that sent an AGENT_MESSAGE. The Worker reads it
// from the control token of the caller's tab, or from the sender that a
// sibling Worker states for a call that it relays, never from the request, so
// no caller can forge it.
//
// The input keeps copies of the sender's provider and title, because the
// sender's row can be purged while the message waits, and the row of a sender
// on a sibling Worker is never here. The title is the one that the sender had
// when it sent the message; the header that the recipient reads states it.
type AgentSender struct {
	TabID    string
	Provider leapmuxv1.AgentProvider
	Title    string
}

// Proto returns the sender as the wire states it, or nil for no sender.
func (s *AgentSender) Proto() *leapmuxv1.AgentMessageSender {
	if s == nil {
		return nil
	}
	return &leapmuxv1.AgentMessageSender{TabId: s.TabID, Provider: s.Provider}
}

// sameSender reports whether two optional senders identify one tab. A
// repeated request must come from the same tab as the input it repeats. The
// title names the sender but does not identify it, so a tab that the user
// renamed between a request and its retry is still the same sender.
func sameSender(a, b *AgentSender) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.TabID == b.TabID && a.Provider == b.Provider
}

type AttachmentMetadata struct {
	Filename string
	MimeType string
	Size     int64
	Order    int32
}

// OrderingMetadata is the projection used to order and admit queued input.
// It carries no message content.
type OrderingMetadata struct {
	ID      string
	AgentID string
	Kind    leapmuxv1.AgentInputKind
	// Order is the durable order_index slot. It breaks ties inside one reorder
	// group; it is not the item's position in the effective order, which
	// SnapshotItem.Position states.
	Order int64
	// ScheduledAt is the durable send-not-before instant. Nil means immediate;
	// a past value is immediate at read time. The reorder group derives from
	// this field and the Worker's clock.
	ScheduledAt    *time.Time
	State          leapmuxv1.AgentInputState
	EditOwner      string
	PrepareContext bool
}

func (item OrderingMetadata) priorityMetadata() OrderingMetadata { return item }

// requiresContextReplacement identifies a plan approval that replaces the blocked provider turn.
func (item OrderingMetadata) requiresContextReplacement() bool {
	return item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION && item.PrepareContext
}

// ItemMetadata adds the complete row metadata to its ordering projection.
type ItemMetadata struct {
	OrderingMetadata
	// Sender is the agent tab that sent an AGENT_MESSAGE, and nil for every
	// other kind. The schema ties the two, so a stored item cannot hold one
	// without the other.
	Sender           *AgentSender
	TargetMode       string
	ReclassifyOnEdit bool
	Error            string
	Version          uint64
	ReservedSeq      int64
	CreatedAt        string
	UpdatedAt        string
}

// StoredItem contains the complete text and provider source used for delivery.
type StoredItem struct {
	ItemMetadata
	Text          string
	SourcePayload []byte
}

type DispatchItem struct {
	StoredItem
	Attachments []Attachment
}

// SnapshotItem contains a preview, never dispatchable stored content.
type SnapshotItem struct {
	ItemMetadata
	TextPreview string
	Metadata    []AttachmentMetadata
	// Position is the item's 1-based place in the effective order at the
	// snapshot's instant. The wire's order field carries it.
	Position int64
	// ReorderGroup is the Worker-derived reorder group at the snapshot's
	// captured instant: empty for immediate/past-due items, the canonical
	// scheduled_at for future ones. The Manager's drain reads it; the wire
	// carries ReorderSegment instead.
	ReorderGroup string
	// ReorderSegment is the item's reorder segment at the snapshot's captured
	// instant (see reorderSegments), or 0 for a locked item. Two items may
	// trade places only when they carry the same non-zero value. Clients
	// compare it and never derive it.
	ReorderSegment uint32
	// CanSteer uses the same predicate as the store's steering guard.
	CanSteer bool
	// CanPreempt uses the same predicate the manager's preemption guard applies:
	// head, queued, unedited, and an active turn to cancel. Unlike CanSteer it does
	// NOT require a steerable turn, nor a kind the model can take mid-turn -- the
	// whole point is the turn the provider can only cancel, not inject into, and
	// the ordinary turn-end drain is what delivers the item afterwards.
	//
	// It excludes the one head that REPLACES the turn, because the store already
	// dispatches that one through the running turn.
	CanPreempt bool
	// EditRefusal is the text of the error that refuses every edit of this
	// item (see editRefusal), or "" when the item allows an edit. It states a
	// rule of the item, never a state that passes, such as a dispatch.
	EditRefusal string
}

// editRefusal returns the error that refuses every edit of an item of kind,
// or nil when the kind allows one. It is the one statement of the rule:
// BeginEdit applies it, and the snapshot states its text in
// SnapshotItem.EditRefusal, so a client never offers an edit that the Worker
// refuses, and needs no rule of its own to say why.
func editRefusal(kind leapmuxv1.AgentInputKind) error {
	if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE {
		return ErrAgentMessageEdit
	}
	return nil
}

type Snapshot struct {
	AgentID             string
	Revision            uint64
	Paused              bool
	PauseReason         leapmuxv1.AgentInputQueuePauseReason
	ActiveTurn          bool
	ActiveTurnSteerable bool
	Items               []SnapshotItem
	// NextDeadline is the earliest scheduled_at among the items that this
	// snapshot shows as future, or zero when none is. The store derives it at
	// the same instant as every Position and ReorderGroup, and the Manager
	// arms its maturity timer from it, so an item that a snapshot shows as
	// future always has a timer.
	NextDeadline time.Time
}

// HeadIndex finds the first item that does not await an echo, or returns -1.
func (s Snapshot) HeadIndex() int {
	for index, item := range s.Items {
		if item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO {
			return index
		}
	}
	return -1
}

// ScheduleEdit is what a queue edit's save does with the item's schedule. The
// zero value keeps the stored schedule, so a deadline that a provider moved
// while the edit was open survives a save that did not change the schedule.
type ScheduleEdit struct {
	// Replace makes the save write At. Without it, At is ignored.
	Replace bool
	// At is the new send-not-before instant, or nil for none.
	At *time.Time
}

// KeepSchedule keeps the stored schedule.
func KeepSchedule() ScheduleEdit { return ScheduleEdit{} }

// ReplaceSchedule writes at as the new schedule, or clears it when at is nil.
func ReplaceSchedule(at *time.Time) ScheduleEdit { return ScheduleEdit{Replace: true, At: at} }

// UpdateRequest describes one owned queue edit and its schedule decision.
type UpdateRequest struct {
	AgentID         string
	InputID         string
	ClientID        string
	ExpectedVersion uint64
	Text            string
	Attachments     []Attachment
	Schedule        ScheduleEdit
}

type NewItem struct {
	ID      string
	AgentID string
	Kind    leapmuxv1.AgentInputKind
	// Sender is the agent tab whose control token enqueued the input, or nil
	// when no agent did: the browser, a terminal tab, the CLI outside a tab,
	// and every input that the Worker creates itself. A user message from a
	// sender becomes an agent message (see admitNewItem).
	Sender           *AgentSender
	Text             string
	TargetMode       string
	PrepareContext   bool
	ReclassifyOnEdit bool
	Attachments      []Attachment
	// ScheduledAt schedules the item; nil means immediate.
	ScheduledAt *time.Time
	// SourcePayload is the provider source an automatic retry carries into the
	// accepted transcript's supplemental content. Empty for ordinary input.
	SourcePayload []byte
}

type PreparedDispatch struct {
	Item        DispatchItem
	ReservedSeq int64
	// Steering preserves the active turn that admitted this input.
	Steering bool
}

type AcceptedTranscript struct {
	ID                             string
	AgentID                        string
	Seq                            int64
	Content                        []byte
	ContentCompression             leapmuxv1.ContentCompression
	SupplementalContent            []byte
	SupplementalContentCompression leapmuxv1.ContentCompression
	AgentProvider                  leapmuxv1.AgentProvider
	MarkType                       leapmuxv1.MarkType
	// SpanLines carries the passthrough span column that Accept wrote into the
	// messages row. The live broadcast must repeat it, or the same bubble
	// renders without its bars now and with them after a reload.
	SpanLines string
	CreatedAt string
}

type DispatchResult struct {
	StartsTurn    bool
	TurnSteerable bool
	SpanLines     string
	Steering      bool
	// SupplementalContent stores the original provider source for frontend extraction.
	SupplementalContent []byte
	AfterAccept         func()
}

// DeliveryError carries a refused dispatch and what the queue must do. A busy
// refusal also carries the provider's turn classification when one exists.
type DeliveryError struct {
	Err                 error
	Outcome             DispatchOutcome
	ActiveTurnSteerable bool
	// PauseReason is the sentence the reader sees when a NotReady outcome pauses
	// the queue. The zero value keeps AGENT_STOPPED, which is what the provider
	// paths mean; a store fault sets STORE_FAULT, because "the agent stopped" is
	// a false statement about a database error.
	PauseReason leapmuxv1.AgentInputQueuePauseReason
}

func (e *DeliveryError) Error() string {
	if e == nil || e.Err == nil {
		return "input delivery failed"
	}
	// The prefix is what a user reads on a held item, so it states the outcome
	// rather than only the provider's wording.
	if prefix := e.Outcome.String(); prefix != "" {
		return prefix + ": " + e.Err.Error()
	}
	return e.Err.Error()
}

// dispatchOutcome reads the outcome a refusal carries. An error that is not a
// DeliveryError is a plain failure: only the queue's own classifier builds one,
// and everything else that reaches the manager already went wrong.
func dispatchOutcome(err error) DispatchOutcome {
	var deliveryErr *DeliveryError
	if errors.As(err, &deliveryErr) {
		return deliveryErr.Outcome
	}
	return DispatchFailed
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Dispatcher interface {
	Dispatch(item DispatchItem) (DispatchResult, error)
	Steer(item DispatchItem) (DispatchResult, error)
	PrepareSteer(agentID string)
	SteerPolicy(agentID string) agent.SteerPolicy
	SupportsSteering(agentID string) bool
	// Interrupt cancels the agent's active turn without any of the stop-side
	// bookkeeping the InterruptAgent RPC applies (queue pause, control
	// withdrawal). Preempt is its only caller: the queued item it unblocks
	// dispatches through the ordinary turn-end drain, and that drain must not
	// find a stop-pause holding it.
	Interrupt(agentID string) error
	// SupportsPreemption answers whether Preempt may be offered for this agent:
	// a running provider that cannot steer but can interrupt.
	SupportsPreemption(agentID string) bool
	// AcceptsKind answers whether this agent accepts the kind at all. Enqueue
	// and Update ask before they store the item, so an input that dispatch can
	// never deliver is refused at the RPC instead of failing the whole queue.
	AcceptsKind(agentID string, kind leapmuxv1.AgentInputKind) bool
}

type CommandClassifier interface {
	Classify(kind leapmuxv1.AgentInputKind, text string) leapmuxv1.AgentInputKind
}

type ExactCommandClassifier struct{}

func (ExactCommandClassifier) Classify(kind leapmuxv1.AgentInputKind, text string) leapmuxv1.AgentInputKind {
	if kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE {
		return kind
	}
	switch strings.TrimSpace(text) {
	case "/clear", "/reset", "/new":
		return leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT
	case "/compact", "/summarize":
		return leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT
	default:
		return kind
	}
}

type Observer interface {
	QueueChanged(snapshot Snapshot)
	InputAccepted(transcript AcceptedTranscript)
}

type NopObserver struct{}

func (NopObserver) QueueChanged(Snapshot)            {}
func (NopObserver) InputAccepted(AcceptedTranscript) {}

func nowText() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
