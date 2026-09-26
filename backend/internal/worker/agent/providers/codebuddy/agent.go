package codebuddy

import (
	"encoding/json"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one CodeBuddy process and the session it serves.
type Agent struct {
	providerkit.Process

	sink agent.ProviderServices
	opts agent.Options

	mu             sync.Mutex
	sessionID      string
	model          string
	permissionMode string
	effort         string
	active         bool
	turnOrder      providerkit.TurnSeq
	activityRev    uint64

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
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)

// codebuddyControlResult is the outcome of one pending control request.
type codebuddyControlResult struct {
	Success        bool
	Error          string
	Mode           string
	PermissionMode string
	Models         []codebuddyModelInfo
	RawResponse    json.RawMessage
}

// codebuddyModelInfo is the model catalog entry an initialize or
// get_available_models response carries.
type codebuddyModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentID returns the worker's identifier for this agent.
func (a *Agent) AgentIDValue() string { return a.AgentID() }

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
	a.activityRev++
	a.mu.Unlock()
	a.PublishTurnActive()

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role:    "user",
			Content: content,
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
func (a *Agent) SteerInput(content string, _ []*leapmuxv1.Attachment) error {
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
		"content_blocks": []map[string]any{{"type": "text", "text": content}},
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
	a.mu.Unlock()
	body, err := json.Marshal(map[string]any{
		"subtype":    "interrupt",
		"session_id": sessionID,
		"reason":     "user",
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
	a.setTurnActive(false)
}

// Wait waits for the process to exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.setTurnActive(false)
	return err
}

// OptionGroups returns every configuration axis this agent currently reports.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	effort := a.effort
	mode := a.permissionMode
	a.mu.Unlock()
	return []*leapmuxv1.AvailableOptionGroup{
		codebuddyEffortGroup(effort),
		codebuddyPermissionModeGroup(mode),
	}
}

// SettingsSnapshot returns the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agent.ConfirmedSettings(map[string]string{
		agent.OptionIDEffort:         a.effort,
		agent.OptionIDPermissionMode: a.permissionMode,
	})
}

// UpdateSettings applies a live change. A permission-mode change sends
// set_permission_mode; an effort change restarts the agent (CodeBuddy takes
// effort only at launch), so it reports RestartRequired.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: agent.OptionSettlements{}}
	live := optionmap.Map{}
	for id, value := range options {
		switch id {
		case agent.OptionIDPermissionMode:
			live[id] = value
		case agent.OptionIDEffort:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		default:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
	}
	if len(live) > 0 {
		if err := a.applyPermissionMode(live[agent.OptionIDPermissionMode]); err != nil {
			for id := range live {
				result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			}
			result.AppliedLive = false
			return result
		}
		value := live[agent.OptionIDPermissionMode]
		result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
	}
	a.mu.Lock()
	surfaced := optionmap.Map{
		agent.OptionIDEffort:         a.effort,
		agent.OptionIDPermissionMode: a.permissionMode,
	}
	a.mu.Unlock()
	result.SurfacedOptions = surfaced
	return result
}

// ClearContext starts a fresh session. CodeBuddy keeps no in-process clear, so
// the service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// sessionHandle returns the current session handle.
