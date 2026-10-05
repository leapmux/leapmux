package qoder

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one Qoder process and the session it serves.
type Agent struct {
	providerkit.Process

	sink agent.ProviderServices
	opts agent.Options

	mu                   sync.Mutex
	sessionID            string
	model                string
	models               []qoderModel
	configuredModelsOnce sync.Once
	configuredModels     []qoderModel
	permissionMode       string
	planMode             bool
	effort               string
	active               bool
	turnOrder            providerkit.TurnSeq
	activityRev          uint64
	capabilities         []string
	childStreamMu        sync.Mutex
	childStreams         map[string]*qoderChildStream
	workflows            map[qoderWorkflowKey]*qoderWorkflowRun
	archiveMu            sync.Mutex
	archiveJobs          map[qoderWorkflowKey]*qoderArchiveJob
	archiveStopped       bool
	archiveRetries       sync.WaitGroup

	// interruptRequested records that the user stopped the RUNNING turn. The
	// `result` that ends that turn spends the note, and a turn end without a
	// `result` drops it. Guarded by mu. See noteInterruptRequested.
	interruptRequested bool

	pendingControlMu sync.Mutex
	pendingControl   map[string]chan<- qoderControlResult
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)

// qoderControlResult is the outcome of one pending control request.
type qoderControlResult struct {
	Success     bool
	Error       string
	Mode        string
	RawResponse json.RawMessage
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.mu.Lock()
	active := a.active
	seq := a.turnOrder.NextTurnSeq()
	a.mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

func (a *Agent) setTurnActive(active bool) {
	a.mu.Lock()
	a.active = active
	if !active {
		// The note belongs to the turn that ends now. A turn can end without a
		// `result` that spends the note: a stop, a process exit, or a failed
		// send. An interrupt can also arrive after handleResult read the note.
		// The next turn must not inherit the note in any of these cases.
		a.interruptRequested = false
	}
	a.activityRev++
	seq := a.turnOrder.NextTurnSeq()
	a.mu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

// SendInput delivers one user message over stdin.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

// SendInputForSession validates the session while constructing the request.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the Qoder process is stopped")
	}
	userContent, err := qoderUserContent(content, attachments)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.active {
		a.mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.active = true
	a.activityRev++
	a.mu.Unlock()
	a.PublishTurnActive()

	raw, err := marshalUserFrame(userContent)
	if err != nil {
		a.setTurnActive(false)
		return err
	}
	if err := a.SendRawInput(raw); err != nil {
		a.setTurnActive(false)
		return providerkit.ClassifyJSONRPCDeliveryError("user", err)
	}
	return nil
}

// marshalUserFrame encodes one stream-json user frame that carries content.
func marshalUserFrame(content any) ([]byte, error) {
	return json.Marshal(UserInputMessage{
		Type:    MessageTypeUser,
		Message: UserInputContent{Role: "user", Content: content},
	})
}

// qoderUserContent builds the content of one stream-json user frame. A prompt
// without attachments stays a plain string.
//
// Qoder reads the LAST content block as the prompt text, and only when that
// block is a text block. Qoder runs its per-prompt work on that text alone:
//
//   - The @-mention reads.
//   - The slash-command check.
//   - The skill listing.
//   - The plan-mode and goal reminders.
//   - The hook outputs.
//   - The to-do reminders.
//
// Thus each attachment goes first, in the order that the user attached it,
// and the prompt text goes last. An image last gives Qoder no prompt text,
// and Qoder then skips all of that work on the turn. A text attachment last
// makes Qoder read the attached file as the prompt.
//
// The prompt text block stays when the text is empty. Qoder then reads an
// empty prompt text and still runs its per-prompt work.
func qoderUserContent(content string, attachments []*leapmuxv1.Attachment) (any, error) {
	classified := agent.ClassifyAttachments(attachments)
	if len(classified) == 0 {
		return content, nil
	}
	blocks := make([]any, 0, len(classified)+1)
	for _, attachment := range classified {
		if err := (qoderProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, qoderTextBlock{Type: "text", Text: providerkit.BuildInlineTextAttachmentBlock(attachment)})
		case agent.AttachmentKindImage:
			blocks = append(blocks, qoderImageBlock{
				Type: "image",
				Source: qoderImageSource{
					Type: "base64", MediaType: attachment.MIMEType,
					Data: base64.StdEncoding.EncodeToString(attachment.Data),
				},
			})
		default:
			return nil, fmt.Errorf("qoder CLI cannot send the attachment %s", attachment.Filename)
		}
	}
	return append(blocks, qoderTextBlock{Type: "text", Text: content}), nil
}

// SupportsSteering reports true when the runtime advertises steering. Qoder
// carries a modelSteering setting and a user-steering injection, so the
// capability is always offered once the process is up.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput sends one user frame during the RUNNING turn. The frame carries
// the same content as a new prompt, attachments included. Qoder queues a user
// frame that arrives during a turn with its content unchanged, and reads that
// content again when the queued prompt starts a turn of its own.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the Qoder process is stopped")
	}
	userContent, err := qoderUserContent(content, attachments)
	if err != nil {
		return err
	}
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	raw, err := marshalUserFrame(userContent)
	if err != nil {
		return err
	}
	return a.SendRawInput(raw)
}

// Interrupt aborts the running turn with Qoder's own interrupt control request.
func (a *Agent) Interrupt() error {
	if a.IsStopped() {
		return fmt.Errorf("the Qoder process is stopped")
	}
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	body, err := json.Marshal(map[string]any{
		"subtype":    contracts.QoderControlRequestSubtypeInterrupt,
		"session_id": sessionID,
	})
	if err != nil {
		return err
	}
	// Take the note BEFORE the request goes out. Qoder writes the `result` of
	// the aborted turn as soon as it reads the request, and the reader goroutine
	// spends the note on that `result`. A note taken after the send can lose
	// that race, and the turn then reads as the failure that its subtype states.
	a.noteInterruptRequested()
	if err := a.sendControlFire(string(body)); err != nil {
		// The request never reached Qoder, so no `result` answers it.
		a.takeInterruptRequest()
		return err
	}
	return nil
}

// noteInterruptRequested records that the USER stopped the running turn, so
// the `result` that ends it carries LeapMux's own completion.
//
// qodercli 1.1.65 reports an aborted turn as `subtype: error_during_execution`
// with `is_error: true` and `errors: ["Operation aborted"]`. It uses the same
// subtype and flag for a genuine failure, so the frame alone cannot tell the
// two apart. LeapMux can, because LeapMux sent the stop.
//
// The note is taken only while a turn runs. Qoder acknowledges an interrupt
// that arrives outside a turn and sends no `result` for it. A note taken there
// would wait and then mislabel the outcome of the NEXT turn.
func (a *Agent) noteInterruptRequested() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active {
		a.interruptRequested = true
	}
}

// takeInterruptRequest reports whether the user stopped the turn that ends now,
// and clears the note. One `result` ends one turn, so that `result` spends it.
func (a *Agent) takeInterruptRequest() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	interrupted := a.interruptRequested
	a.interruptRequested = false
	return interrupted
}

// Stop terminates the process.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Process.Stop()
	a.stopWorkflowArchiveRetries()
	a.setTurnActive(false)
}

// Wait waits for the process to exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopWorkflowArchiveRetries()
	a.setTurnActive(false)
	return err
}

// ClearContext starts a fresh session. Qoder keeps no in-process clear, so the
// service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}
