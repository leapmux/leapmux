package letta

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one `letta server --listen` process and the one conversation
// LeapMux drives in it.
//
// The process prints two ready lines and then speaks protocol_v2 over one
// WebSocket. The agent embeds Process for the lifecycle and routes
// conversation over the socket (rpc.go), with deltas dispatched in output.go.
type Agent struct {
	providerkit.Process

	sink       agent.ProviderServices
	workingDir string
	clock      quartz.Clock
	wsURL      string
	ws         *wsConn

	dispatchMu sync.Mutex
	sendMu     sync.Mutex

	// --- guarded by Mu ---

	agentID        string
	conversationID string
	turnActive     bool
	stopped        bool
	settings       lettaSettings
	catalog        lettaCatalog
	tools          map[string]*lettaTool
	controls       map[string]*lettaPendingControl
	generation     providerkit.GenerationBuffer
	// runtimeReady holds the open runtime_start handshake, until the response
	// settles it. Start waits on it before it returns the agent.
	runtimeReady *runtimeWaiter
}

var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.InputSteerer = (*Agent)(nil)
)

// lettaSendWait limits how long SendInput waits for the server to accept input.
const lettaSendWait = 30 * time.Second

// errAgentStopped reports that the agent's process already ended.
var errAgentStopped = errors.New("the Letta Code server has stopped")

// lettaSettings is the live configuration of the conversation.
type lettaSettings struct {
	model          string
	reasoningLevel string
	permissionMode string
}

// lettaCatalog is the model list the server reported.
type lettaCatalog struct {
	models []lettaModel
}

// lettaModel is one entry of list_models.
type lettaModel struct {
	id          string
	displayName string
}

// lettaTool is one open tool call.
type lettaTool struct {
	id     string
	name   string
	spanID string
}

// lettaPendingControl is one published control request awaiting an answer.
type lettaPendingControl struct {
	requestID  string
	kind       lettaControlKind
	toolCallID string
}

// lettaControlKind distinguishes a permission request from a question.
type lettaControlKind string

const (
	lettaControlPermission lettaControlKind = "permission"
	lettaControlAskUser    lettaControlKind = "ask_user"
)

// SendInput delivers a user message to the conversation.
//
// It returns once the input frame is written, and never waits for the turn. A
// running turn refuses the input with ErrAgentBusy, so the LeapMux input queue
// holds it; Letta itself would queue it and run it as a new turn after the
// current one, which LeapMux must show as its own turn.
//
// It never sends before the runtime identity exists. Start waits for the
// runtime_start response, and sendInput refuses an empty scope as a backstop:
// the App Server drops a scope-less input without an error.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments)
}

// SendInputForSession delivers a user message when the conversation it states
// is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments)
}

// HandleOutput feeds one stdout line into the dispatcher. The App Server
// speaks over its WebSocket; stdout carries the ready line and logs.
func (a *Agent) HandleOutput(content []byte) {
	a.handleFrame(content)
}

// SteerInput is unsupported: Letta queues a mid-turn message as a new turn.
func (a *Agent) SteerInput(string, []*leapmuxv1.Attachment) error {
	return agent.ErrSteeringUnsupported
}

// SupportsSteering reports false. Letta queues a mid-turn user message and runs
// it as a NEW turn after the current one ends; it cannot inject into the
// running turn.
func (a *Agent) SupportsSteering() bool { return false }

// sendInput writes one input command.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	payload, err := buildUserMessages(content, attachments)
	if err != nil {
		return err
	}

	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.conversationID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.stopped {
		a.Mu.Unlock()
		return errAgentStopped
	}
	scope := runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
	active := a.turnActive
	a.Mu.Unlock()
	if active {
		return agent.ErrAgentBusy
	}
	// A scope-less input is not a delivery the server can refuse: it is
	// dropped with no reply at all. Fail it here instead.
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the Letta runtime identity is not known yet; the input cannot be addressed")
	}

	a.armTurn()
	cmd := newLettaCommand("input", a.nextRequestID())
	cmd.Runtime = &scope
	cmd.Payload = map[string]any{
		"kind":     "create_message",
		"messages": payload,
	}
	// One line per delivered input. The scope fields name the runtime the
	// frame addresses, which is the difference between a turn and a silent
	// drop on this protocol.
	slog.Info("letta: send input",
		"agent_id", a.AgentID(),
		"request_id", cmd.RequestID,
		"runtime_agent_id", scope.AgentID,
		"runtime_conversation_id", scope.ConversationID,
		"content_len", len(content))
	if err := a.sendCommand(cmd); err != nil {
		slog.Warn("letta: input send failed", "agent_id", a.AgentID(), "error", err)
		a.disarmTurn()
		return err
	}
	return nil
}

// runtime returns the ConversationRuntimeScope of this conversation.
func (a *Agent) runtime() runtimeScope {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
}

// buildUserMessages encodes a user message as Letta's MessageCreate list.
func buildUserMessages(content string, attachments []*leapmuxv1.Attachment) ([]any, error) {
	blocks := []any{}
	text := strings.TrimSpace(content)
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, att := range attachments {
		if att == nil {
			continue
		}
		if strings.HasPrefix(att.GetMimeType(), "image/") {
			blocks = append(blocks, map[string]any{
				"type":       "image",
				"media_type": att.GetMimeType(),
				"data":       att.GetData(),
			})
			continue
		}
		if s := strings.TrimSpace(string(att.GetData())); s != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return []any{map[string]any{"role": "user", "content": blocks}}, nil
}

// armTurn marks a turn active and publishes the flag. A repeat of the current
// state publishes nothing, so a frame that is not a turn signal moves nothing.
func (a *Agent) armTurn() {
	a.Mu.Lock()
	if a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = true
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: true}, a.NextTurnSeq())
}

// disarmTurn marks no turn active and publishes the flag. A repeat of the
// current state publishes nothing.
func (a *Agent) disarmTurn() {
	a.Mu.Lock()
	if !a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = false
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: false}, a.NextTurnSeq())
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive, Steerable: false}
	a.Mu.Unlock()
	a.sink.SetTurnState(state, a.NextTurnSeq())
	return state
}

// Interrupt aborts the running turn with abort_message.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if !active {
		return nil
	}
	cmd := newLettaCommand("abort_message", a.nextRequestID())
	scope := a.runtime()
	cmd.Runtime = &scope
	return a.sendCommand(cmd)
}

// ClearContext starts a fresh conversation. Letta has no in-process clear that
// LeapMux can address, so this reports unsupported and the worker restarts.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// OptionGroups returns the live configuration axes.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	settings := a.settings
	catalog := a.catalog
	a.Mu.Unlock()

	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, 2)
	// The model group is always present: the settings menu offers it even when
	// the session reported no catalog yet. The option list must HOLD the
	// current value, because the chip resolves its label by looking that value
	// up in the options -- a list that omits it draws the group label instead
	// of the model the session is running.
	models := catalog.models
	if len(models) == 0 {
		seen := map[string]bool{}
		if settings.model != "" {
			models = append(models, lettaModel{id: settings.model, displayName: settings.model})
			seen[settings.model] = true
		}
		for _, m := range defaultModels {
			if !seen[m.Id] {
				models = append(models, lettaModel{id: m.Id, displayName: m.DisplayName})
				seen[m.Id] = true
			}
		}
	}
	groups = append(groups, lettaModelGroup(models, settings.model))
	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Id:           agent.OptionIDPermissionMode,
		Label:        PermissionModeLabel,
		CurrentValue: settings.permissionMode,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options:      permissionModeGroup.GetOptions(),
	})
	return groups
}

// SettingsSnapshot reports the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	groups := a.OptionGroups()
	values := map[string]string{}
	for _, g := range groups {
		if g.GetId() != "" && g.GetCurrentValue() != "" {
			values[g.GetId()] = g.GetCurrentValue()
		}
	}
	return agent.ConfirmedSettings(values)
}

// UpdateSettings applies each included non-empty option to the running agent.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	agentID := a.agentID
	a.Mu.Unlock()
	if agentID == "" {
		return agent.RestartRequiredSettings(options)
	}
	patch := map[string]any{}
	for id, value := range options {
		if value == "" {
			continue
		}
		switch id {
		case agent.OptionIDModel:
			patch["model"] = value
		case agent.OptionIDEffort:
			patch["reasoning_effort"] = value
		}
	}
	if len(patch) == 0 {
		// A permission-mode change reaches runtime_start only; the live server
		// takes no mid-run mode command, so it needs a restart.
		if _, ok := options[agent.OptionIDPermissionMode]; ok {
			return agent.RestartRequiredSettings(options)
		}
		return agent.ConfirmedSettings(nil)
	}
	cmd := newLettaCommand("update_model", a.nextRequestID())
	scope := a.runtime()
	cmd.Runtime = &scope
	cmd.Payload = patch
	if err := a.sendCommand(cmd); err != nil {
		return agent.RestartRequiredSettings(options)
	}
	a.Mu.Lock()
	if v, ok := patch["model"].(string); ok {
		a.settings.model = v
	}
	if v, ok := patch["reasoning_effort"].(string); ok {
		a.settings.reasoningLevel = v
	}
	a.Mu.Unlock()
	return agent.ConfirmedSettings(options)
}

// Stop ends the process.
func (a *Agent) Stop() {
	a.Mu.Lock()
	a.stopped = true
	a.Mu.Unlock()
	a.Process.Stop()
}

// nextRequestID issues a command correlation id.
func (a *Agent) nextRequestID() string {
	return fmt.Sprintf("leapmux-%d", a.NextTurnSeq())
}
