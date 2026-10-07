package codebuddy

import (
	"encoding/json"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one CodeBuddy process and the session it serves.
type Agent struct {
	providerkit.Process

	sink agent.ProviderServices
	opts agent.Options

	mu                        sync.Mutex
	sessionID                 string
	model                     string
	models                    []codebuddyModelInfo
	permissionMode            string
	effort                    string
	active                    bool
	assistantTextStreamed     bool
	pendingPlanModeTools      map[string]agent.PlanModeControlKind
	awaitingRejectedPlanInit  bool
	nativeTurnRestartRequired bool
	turnOrder                 providerkit.TurnSeq
	activityRev               uint64
	turnToolCalls             map[string]struct{}
	interruptRequests         map[uint64]struct{}
	interruptAttempt          uint64
	// openPermissions is the can_use_tool requests that CodeBuddy still waits on, so
	// an interrupt can refuse each one (see refuseOpenPermissions).
	openPermissions map[string]struct{}
	// permissionInterruptPending holds the global stop until the main run ends.
	permissionInterruptPending bool
	permissionWriteMu          sync.Mutex

	// hasGoalCommand records whether the running CLI advertises /goal in its
	// `slash_commands` list. The goal controls require the CLI's own
	// statement, never on a version table.
	hasGoalCommand bool
	// goalCommandKnown records whether the init frame arrived at all. Absent is
	// UNKNOWN, which still allows the goal: a cold-started process can accept
	// the command before its first stdout frame.
	goalCommandKnown bool

	// pendingControl routes a control_response to the caller waiting on its
	// request id. See sendControlAndWait.
	pendingControlMu sync.Mutex
	pendingControl   map[string]chan<- codebuddyControlResult
	tasks            codebuddyTaskIndex
	archiveMu        sync.Mutex
	archiveJobs      map[string]*codebuddyArchiveJob
	archiveChildJobs map[string]*codebuddyArchiveJob
	archiveStopped   bool
	archiveRetries   sync.WaitGroup
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.NativeTurnRestarter = (*Agent)(nil)

// codebuddyControlResult is the outcome of one pending control request.
type codebuddyControlResult struct {
	// Answered is true when CodeBuddy sent a control_response for the request.
	// A request with no answer may still have reached CodeBuddy.
	Answered        bool
	Success         bool
	Error           string
	Mode            string
	Model           string
	Models          []codebuddyModelInfo
	HasModelCatalog bool
	// Steered and SteerReason are the answer to a steer request. Steered is
	// nil when the answer states no outcome.
	Steered     *bool
	SteerReason string
}

// codebuddyModelInfo is one get_available_models entry.
type codebuddyModelInfo struct {
	ID          string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AgentID returns the worker's identifier for this agent.
func (a *Agent) AgentIDValue() string { return a.AgentID() }

// NativeTurnRestartRequired reports a native Plan tool change that launch
// options must carry into the next prompt of this session.
func (a *Agent) NativeTurnRestartRequired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nativeTurnRestartRequired
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
	if active && !a.active {
		a.turnToolCalls = nil
		a.interruptRequests = nil
	}
	a.active = active
	if !active {
		clear(a.openPermissions)
		a.permissionInterruptPending = false
	}
	a.activityRev++
	seq := a.turnOrder.NextTurnSeq()
	a.mu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

// SendInput delivers one user message over stdin. The write IS the delivery.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

// SendInputForSession validates the session while constructing the request.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
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
	a.assistantTextStreamed = false
	a.turnToolCalls = nil
	a.interruptRequests = nil
	a.activityRev++
	a.mu.Unlock()
	a.PublishTurnActive()

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role:    "user",
			Content: codebuddyUserContent(content, attachments),
		},
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

// SupportsSteering reports true: CodeBuddy carries a `steer` control request
// that injects user content into a running turn.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput injects content into the RUNNING turn through control_request/steer,
// and it returns the outcome that CodeBuddy answers.
//
// The steer drain of CodeBuddy keeps the text of the blocks only (ny and ly in
// 2.160.0). An image, a PDF or a binary file would lose its bytes there, and its
// label would still tell the model that a file is attached. SteerInput
// therefore refuses an input that carries any file but a text file as
// unsupported, and the worker sends that input as the next turn, files and all.
// The refusal depends on the input alone, so it comes before the turn check.
//
// See codebuddySteerOutcome for how the answer of CodeBuddy maps to the result.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
	}
	classified := agent.ClassifyAttachments(attachments)
	for _, attachment := range classified {
		if attachment.Kind != agent.AttachmentKindText {
			return fmt.Errorf("%w: CodeBuddy steers a running turn with text only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
	}
	a.mu.Lock()
	active := a.active
	sessionID := a.sessionID
	a.mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	body, err := json.Marshal(map[string]any{
		"subtype":        "steer",
		"session_id":     sessionID,
		"content_blocks": codebuddyContentBlocks(content, classified),
	})
	if err != nil {
		return err
	}
	return codebuddySteerOutcome(a.sendControlAndWait(string(body), a.APITimeout()))
}

// codebuddySteerOutcome maps the answer of CodeBuddy to a steer request (its
// steer handler q4 in 2.160.0) to the error that the input queue of the worker
// reads:
//   - steered:true. The drain holds the steer for the running turn. The steer
//     succeeds.
//   - steered:false with the reason "idle" or "stale". The turn that the steer
//     was for is over. ErrNoActiveTurn makes the queue send the input as the
//     next turn.
//   - steered:false with any other reason, or with none. CodeBuddy refuses the
//     content. It refuses a steer whose text starts with a slash command. It
//     refuses a steer with no text. ErrSteeringUnsupported makes the queue send
//     the input as the next turn, where a slash command runs.
//   - An error answer. CodeBuddy refused the request before its drain took the
//     steer. The steer fails with the message of CodeBuddy.
//   - No answer, or an answer that states no outcome. The drain may hold the
//     steer, so a resend could duplicate it. ErrDeliveryUncertain makes the
//     queue ask before it sends the input again.
func codebuddySteerOutcome(resp codebuddyControlResult, err error) error {
	switch {
	case err != nil && !resp.Answered:
		return fmt.Errorf("%w: CodeBuddy did not answer the steer: %w", agent.ErrDeliveryUncertain, err)
	case err != nil:
		return fmt.Errorf("CodeBuddy refused the steer request: %w", err)
	case resp.Steered == nil:
		return fmt.Errorf("%w: CodeBuddy answered the steer without its outcome", agent.ErrDeliveryUncertain)
	case *resp.Steered:
		return nil
	case resp.SteerReason == codebuddySteerReasonIdle || resp.SteerReason == codebuddySteerReasonStale:
		return fmt.Errorf("%w: CodeBuddy found no running turn for the steer (%s)", agent.ErrNoActiveTurn, resp.SteerReason)
	case resp.SteerReason != "":
		return fmt.Errorf("%w: CodeBuddy refused to add the steer to the running turn (%s)", agent.ErrSteeringUnsupported, resp.SteerReason)
	default:
		return fmt.Errorf("%w: CodeBuddy refused to add the steer to the running turn", agent.ErrSteeringUnsupported)
	}
}

// Interrupt stops the turn through a waiting permission or a control request.
// CodeBuddy's permission refusal carries interrupt:true and stops the native run.
// Send the global stop after the native run ends, before the next input enters.
func (a *Agent) Interrupt() error {
	a.permissionWriteMu.Lock()
	defer a.permissionWriteMu.Unlock()
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
	}
	a.mu.Lock()
	if a.permissionInterruptPending {
		a.mu.Unlock()
		return nil
	}
	sessionID := a.sessionID
	revision := a.activityRev
	requested := a.active
	permissionStop := requested && len(a.openPermissions) > 0
	a.permissionInterruptPending = permissionStop
	a.interruptAttempt++
	attempt := a.interruptAttempt
	if requested {
		if a.interruptRequests == nil {
			a.interruptRequests = make(map[uint64]struct{})
		}
		a.interruptRequests[attempt] = struct{}{}
	}
	a.mu.Unlock()
	forgetFailedAttempt := func() {
		a.mu.Lock()
		if requested && a.active && a.activityRev == revision {
			delete(a.interruptRequests, attempt)
			a.permissionInterruptPending = false
		}
		a.mu.Unlock()
	}
	if permissionStop {
		refused, err := a.refuseOpenPermissions()
		if err != nil && !refused {
			forgetFailedAttempt()
		}
		if err != nil || refused {
			return err
		}
		a.mu.Lock()
		a.permissionInterruptPending = false
		a.mu.Unlock()
	}
	if err := a.sendInterruptControl(sessionID); err != nil {
		forgetFailedAttempt()
		return err
	}
	return nil
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

// ClearContext starts a fresh session. CodeBuddy keeps no in-process clear, so
// the service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// sessionHandle returns the current session handle.
