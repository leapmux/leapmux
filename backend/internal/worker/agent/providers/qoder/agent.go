package qoder

import (
	"encoding/json"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one Qoder process and the session it serves.
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
	capabilities   []string

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

func (a *Agent) sendInputForSession(expected *string, content string, _ []*leapmuxv1.Attachment) error {
	if a.IsStopped() {
		return fmt.Errorf("the Qoder process is stopped")
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
		Message: UserInputContent{Role: "user", Content: content},
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
		qoderEffortGroup(effort),
		qoderPermissionModeGroup(mode),
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
// set_permission_mode; an effort change restarts the agent (Qoder takes
// `--reasoning-effort` at launch), so it reports RestartRequired.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: agent.OptionSettlements{}}
	if value, ok := options[agent.OptionIDPermissionMode]; ok {
		if err := a.applyPermissionMode(value); err != nil {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		} else {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	for id := range options {
		// An effort change needs the launch flag, so it settles only after a
		// restart. Every other axis this agent does not apply live says the
		// same.
		if id != agent.OptionIDPermissionMode {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
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

// ClearContext starts a fresh session. Qoder keeps no in-process clear, so the
// service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}
