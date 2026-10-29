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

	mu                       sync.Mutex
	sessionID                string
	model                    string
	models                   []qoderModel
	modelsReported           bool
	configuredModelsOnce     sync.Once
	configuredModels         []qoderModel
	permissionMode           string
	planMode                 bool
	effort                   string
	thinking                 string   // The Extended Thinking value at launch; Qoder reads it only then.
	outputStyle              string   // The Output Style value at launch; empty or auto sends no flag.
	outputStyles             []string // The styles that the initialize reply lists, under mu.
	runningOutputStyle       string   // The style that the initialize reply reports, under mu.
	active                   bool
	turnOrder                providerkit.TurnSeq
	activityRev              uint64
	capabilities             []string
	childStreamMu            sync.Mutex
	childStreams             map[string]*qoderChildStream
	childTaskIDs             map[string]string // Native task ID of each running task child, by its spawn tool_use_id, under mu.
	rootStreamKinds          map[int]string    // Native block kinds for live progress, under mu.
	storedInstructionWarning bool
	notifications            map[string]*qoderNotificationState // The empty key identifies the root transcript. Guarded by mu.
	workflows                map[qoderWorkflowKey]*qoderWorkflowRun
	archiveMu                sync.Mutex
	archiveJobs              map[qoderWorkflowKey]*qoderArchiveJob
	archiveStopped           bool
	archiveRetries           sync.WaitGroup

	// interruptRequested records the user's stop for the active turn, under mu.
	// Its result consumes the flag. A turn end without a result clears it also.
	interruptRequested bool

	// compactionTurn records that the active turn runs Qoder's native /compact
	// command, under mu. Only an input that starts a turn sets it. Its result
	// or the process exit consumes the flag, and every other turn end clears it.
	compactionTurn bool

	controls providerkit.Correlator[string]

	// usageRefresh wakes refreshUsageLoop. Start creates it, so an agent built
	// without a process sends no quota request. It never changes afterwards.
	usageRefresh chan struct{}
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.ChildInterrupter = (*Agent)(nil)

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
		// Clear the stop flag when a turn ends without a result.
		// A stop, process exit, or failed send can cause that end.
		// An interrupt can also arrive after handleResult reads the flag.
		// The next turn must not inherit the flag in any of these cases.
		a.interruptRequested = false
		// The next turn must not inherit the compaction mark either.
		a.compactionTurn = false
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
	// Qoder runs a /compact prompt as its native compaction. An input with an
	// attachment is an ordinary prompt.
	command, _, slash := providerkit.SlashCommand(content)
	compaction := slash && command == "compact" && len(attachments) == 0
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
	a.compactionTurn = compaction
	a.activityRev++
	a.mu.Unlock()
	a.PublishTurnActive()

	raw, err := marshalUserFrame(userContent)
	if err != nil {
		a.setTurnActive(false)
		return err
	}
	if err := a.SendRawInput(raw, agent.StopContext{}); err != nil {
		a.setTurnActive(false)
		return providerkit.ClassifyJSONRPCDeliveryError("user", err)
	}
	return nil
}

// marshalUserFrame encodes one stream-json user frame that carries content.
func marshalUserFrame(content any) ([]byte, error) {
	return json.Marshal(UserInputMessage{
		Type:    contracts.QoderFrameKindUser,
		Message: UserInputContent{Role: "user", Content: content},
	})
}

// qoderUserContent builds the content of one stream-json user frame. A prompt
// without attachments stays a plain string.
//
// Qoder reads the last content block as the prompt text, and only when that
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
		case contracts.AttachmentMediaKindText:
			blocks = append(blocks, qoderTextBlock{Type: "text", Text: providerkit.BuildInlineTextAttachmentBlock(attachment)})
		case contracts.AttachmentMediaKindImage:
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

// SupportsSteering reports Qoder's native input support during an active turn.
// The runtime accepts these user frames without capability discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput sends one user frame during the active turn, with its attachments.
// Qoder keeps queued content unchanged and reads it when the queued prompt starts its own turn.
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
	return a.SendRawInput(raw, agent.StopContext{})
}

// Interrupt aborts the running turn with Qoder's own interrupt control request.
func (a *Agent) Interrupt(stop agent.StopContext) error {
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
	// Record the stop before the send. Qoder can return the aborted result as soon as it reads the request.
	// The reader consumes the flag for that result.
	// A flag set after the send could arrive too late and leave the turn marked as failed.
	a.noteInterruptRequested()
	if err := a.sendControlFire(string(body)); err != nil {
		// The request never reached Qoder, so no `result` answers it.
		a.takeInterruptRequest()
		return err
	}
	return nil
}

// noteInterruptRequested records the user's stop so the native result carries an interrupted completion.
//
// qodercli 1.1.65 reports an aborted turn as `subtype: error_during_execution`
// with `is_error: true` and `errors: ["Operation aborted"]`. It uses the same
// subtype and flag for a genuine failure, so the frame alone cannot tell the
// two apart. LeapMux can, because LeapMux sent the stop.
//
// Record the flag only while a turn runs.
// Qoder acknowledges an interrupt outside a turn but sends no result for it.
// A flag set there would incorrectly mark the next turn as interrupted.
func (a *Agent) noteInterruptRequested() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active {
		a.interruptRequested = true
	}
}

// takeInterruptRequest reads and clears the user's stop flag for the turn that ends now.
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
	a.releaseTurnAfterExit()
}

// Wait waits for the process to exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopWorkflowArchiveRetries()
	a.releaseTurnAfterExit()
	return err
}

// releaseTurnAfterExit releases the turn after the process exits. The exit
// ends a /compact turn that is still running, and that compaction never
// retries. Stop and Wait run it after the reader has drained, so no result
// follows, and the first of them consumes the compaction mark.
func (a *Agent) releaseTurnAfterExit() {
	a.mu.Lock()
	compaction := a.compactionTurn
	a.compactionTurn = false
	a.mu.Unlock()
	if compaction {
		providerkit.ApplyTurnFailure(a.sink, providerkit.CompactionEnd(), nil)
	}
	a.setTurnActive(false)
}

// ClearContext starts a fresh session. Qoder keeps no in-process clear, so the
// service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}
