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

	// hasGoalCommand records whether the running CLI advertises /goal in its
	// `slash_commands` list. It gates the goal controls on the CLI's own
	// statement, never on a version table.
	hasGoalCommand bool
	// goalCommandKnown records whether the init frame arrived at all. Absent is
	// UNKNOWN, which still allows the goal: a cold-started process can accept
	// the command before its first stdout frame.
	goalCommandKnown bool

	// pendingControl routes a control_response to the caller waiting on its
	// request id. See control.go.
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
	Success         bool
	Error           string
	Mode            string
	Model           string
	Models          []codebuddyModelInfo
	HasModelCatalog bool
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

// SteerInput injects content into the RUNNING turn through control_request/steer.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
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
		"content_blocks": codebuddyContentBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return err
	}
	return a.sendControlFire(string(body))
}

// Interrupt aborts the running turn with CodeBuddy's own control_request.
func (a *Agent) Interrupt() error {
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
	}
	a.mu.Lock()
	sessionID := a.sessionID
	revision := a.activityRev
	requested := a.active
	a.interruptAttempt++
	attempt := a.interruptAttempt
	if requested {
		if a.interruptRequests == nil {
			a.interruptRequests = make(map[uint64]struct{})
		}
		a.interruptRequests[attempt] = struct{}{}
	}
	a.mu.Unlock()
	body, err := json.Marshal(map[string]any{
		"subtype":    "interrupt",
		"session_id": sessionID,
		"reason":     "user",
	})
	if err != nil {
		return err
	}
	if err := a.sendControlFire(string(body)); err != nil {
		a.mu.Lock()
		if requested && a.active && a.activityRev == revision {
			delete(a.interruptRequests, attempt)
		}
		a.mu.Unlock()
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
