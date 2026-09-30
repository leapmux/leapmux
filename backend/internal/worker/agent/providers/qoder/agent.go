package qoder

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

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

	msg := UserInputMessage{
		Type:    MessageTypeUser,
		Message: UserInputContent{Role: "user", Content: userContent},
	}
	raw, err := json.Marshal(msg)
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

// qoderUserContent builds the text and image blocks Qoder's stream input reads.
func qoderUserContent(content string, attachments []*leapmuxv1.Attachment) (any, error) {
	classified := agent.ClassifyAttachments(attachments)
	if len(classified) == 0 {
		return content, nil
	}
	blocks := []any{qoderTextBlock{Type: "text", Text: content}}
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
	return blocks, nil
}

// SupportsSteering reports true when the runtime advertises steering. Qoder
// carries a modelSteering setting and a user-steering injection, so the
// capability is always offered once the process is up.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput injects content into the RUNNING turn as a user-steering update.
func (a *Agent) SteerInput(content string, _ []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the Qoder process is stopped")
	}
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	msg := UserInputMessage{
		Type:    MessageTypeUser,
		Message: UserInputContent{Role: "user", Content: content},
	}
	raw, err := json.Marshal(msg)
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
		"subtype":    "interrupt",
		"session_id": sessionID,
	})
	if err != nil {
		return err
	}
	return a.sendControlFire(string(body))
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
