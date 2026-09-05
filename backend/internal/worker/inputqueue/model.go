package inputqueue

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
// that cause lifts it again. A pause writer always overwrites the owner, so
// the newest cause owns the pause: an archive resume, or the end of a planned
// restart, then leaves a pause that a later crash or the user created. The
// values persist in agent_input_queue_state.pause_owner.
const (
	pauseOwnerNone = iota
	pauseOwnerManual
	pauseOwnerArchive
	pauseOwnerPlannedRestart
	pauseOwnerDelivery
	pauseOwnerAgentStopped
	pauseOwnerRecovery
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
	ErrManagerStopped        = errors.New("agent input queue is stopped")
	// ErrPlannedRestart refuses an explicit dispatch while the Worker replaces
	// the agent process. The durable pause_owner records the restart, so the
	// refusal survives a Worker restart that leaves the marker behind.
	ErrPlannedRestart = errors.New("agent input queue waits for a planned agent restart")
	// ErrDispatchNotReady marks a dispatch failure that never reached the
	// provider. The manager returns the item to the queue instead of storing a
	// permanent failure, so an agent that starts later delivers it.
	ErrDispatchNotReady = errors.New("agent cannot accept input yet")
)

type Attachment struct {
	Filename string
	MimeType string
	Data     []byte
}

type AttachmentMetadata struct {
	Filename string
	MimeType string
	Size     int64
	Order    int32
}

type Item struct {
	ID               string
	AgentID          string
	Kind             leapmuxv1.AgentInputKind
	Text             string
	TargetMode       string
	PrepareContext   bool
	ReclassifyOnEdit bool
	Attachments      []Attachment
	Metadata         []AttachmentMetadata
	Order            int64
	State            leapmuxv1.AgentInputState
	Error            string
	EditOwner        string
	Version          uint64
	ReservedSeq      int64
	// CanSteer answers whether SteerQueuedAgentInput accepts this item right
	// now. The Worker computes it from the same predicate the store's steering
	// guard applies, so the browser never offers an operation the Worker
	// refuses. Only a snapshot carries it.
	CanSteer  bool
	CreatedAt string
	UpdatedAt string
}

type Snapshot struct {
	AgentID        string
	Revision       uint64
	Paused         bool
	PauseReason    leapmuxv1.AgentInputQueuePauseReason
	ActiveTurn     bool
	ActiveTurnKind leapmuxv1.AgentInputKind
	Items          []Item
}

type NewItem struct {
	ID               string
	AgentID          string
	Kind             leapmuxv1.AgentInputKind
	Text             string
	TargetMode       string
	PrepareContext   bool
	ReclassifyOnEdit bool
	Attachments      []Attachment
}

type PreparedDispatch struct {
	Item        Item
	ReservedSeq int64
}

type AcceptedTranscript struct {
	ID                 string
	AgentID            string
	Seq                int64
	Content            []byte
	ContentCompression leapmuxv1.ContentCompression
	AgentProvider      leapmuxv1.AgentProvider
	MarkType           leapmuxv1.MarkType
	// SpanLines carries the passthrough span column that Accept wrote into the
	// messages row. The live broadcast must repeat it, or the same bubble
	// renders without its bars now and with them after a reload.
	SpanLines string
	CreatedAt string
}

type DispatchResult struct {
	StartsTurn  bool
	SpanLines   string
	Steering    bool
	AfterAccept func()
}

type DeliveryError struct {
	Err       error
	Uncertain bool
}

func (e *DeliveryError) Error() string {
	if e == nil || e.Err == nil {
		return "input delivery failed"
	}
	return e.Err.Error()
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Dispatcher interface {
	Dispatch(item Item) (DispatchResult, error)
	Steer(item Item) (DispatchResult, error)
	SupportsSteering(agentID string) bool
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
