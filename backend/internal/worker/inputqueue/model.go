package inputqueue

import (
	"errors"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	MaxItems                  = 100
	MaxItemBytes              = 10 * 1024 * 1024
	MaxQueueAttachmentBytes   = 100 * 1024 * 1024
	maxQueueIdentityByteCount = 256
	// MaxAttachmentsPerItem limits metadata growth in authoritative snapshots.
	MaxAttachmentsPerItem = 100
	// MaxAttachmentFilenameBytes accepts names beyond common filesystem limits
	// while it keeps a maximum-size queue snapshot inside the wire budget.
	MaxAttachmentFilenameBytes = 1024
	// MaxAttachmentMIMETypeBytes limits untrusted media type metadata.
	MaxAttachmentMIMETypeBytes    = 256
	snapshotTextPreviewCharacters = 1024
)

var (
	ErrNotFound              = errors.New("queued agent input not found")
	ErrConflict              = errors.New("queued agent input conflicts with existing data")
	ErrEditOwned             = errors.New("queued agent input is edited by another client")
	ErrEditOwnerMismatch     = errors.New("queued agent input edit owner does not match")
	ErrVersionConflict       = errors.New("queued agent input version conflict")
	ErrQueueFull             = errors.New("agent input queue holds 100 items")
	ErrItemTooLarge          = errors.New("queued agent input exceeds 10 MiB")
	ErrQueueAttachmentsLarge = errors.New("agent input queue attachments exceed 100 MiB")
	ErrInvalidInput          = errors.New("invalid queued agent input")
	ErrNotHead               = errors.New("only the queue head supports this operation")
	ErrRetryState            = errors.New("queue head is not failed or delivery uncertain")
	ErrUncertainConfirmation = errors.New("retrying delivery-uncertain input requires confirmation")
	ErrTurnEnded             = errors.New("active turn ended before steering")
	ErrSteeringState         = errors.New("queue state does not permit steering")
	ErrSteeringUnsupported   = errors.New("agent provider does not support steering")
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
	CreatedAt        string
	UpdatedAt        string
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
	CreatedAt          string
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
