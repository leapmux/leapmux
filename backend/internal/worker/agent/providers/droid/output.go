package droid

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// droidAttachmentLabel names the provider in an attachment refusal.
const droidAttachmentLabel = "Factory Droid"

// isDroidRawInterrupt reports whether a raw frame is LeapMux's own interrupt
// marker for Droid. The browser interrupts through the InterruptAgent call; the
// frame is for a caller of SendAgentRawMessage.
func isDroidRawInterrupt(content []byte) bool {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &head); err != nil {
		return false
	}
	return head.Type == "interrupt"
}

// HandleOutput feeds one stdout line into the dispatcher.
func (a *Agent) HandleOutput(content []byte) {
	a.handleFrame(content)
}

// handleFrame dispatches one stream-jsonrpc message. It runs on the process's
// reader goroutine and on HandleOutput.
func (a *Agent) handleFrame(line []byte) {
	if a.IsDiscardingOutput() {
		return
	}
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return
	}
	var env droidEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("droid: unparseable line", "agent_id", a.AgentID(), "error", err)
		return
	}

	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	switch env.Type {
	case droidTypeNotification:
		a.handleNotification(line, &env)
	case droidTypeRequest:
		a.handleServerRequest(line, &env)
	case droidTypeResponse:
		// The initialize_session response carries the session id every later
		// request needs. The rest of the worker's own requests are
		// fire-and-forget.
		if env.ID == droidInitRequestID {
			a.adoptSessionID(&env)
		}
		if env.Error != nil {
			slog.Debug("droid: request failed", "agent_id", a.AgentID(), "error", env.Error)
		}
	default:
		slog.Debug("droid: unknown envelope type", "agent_id", a.AgentID(), "type", env.Type)
	}
}

// droidNotification is the params of droid.session_notification.
type droidNotification struct {
	SessionID    string          `json:"sessionId"`
	Notification json.RawMessage `json:"notification"`
}

// droidNotifHead is the `type` discriminator of a notification payload.
type droidNotifHead struct {
	Type string `json:"type"`
}

// handleNotification dispatches one session notification. The worker persists
// the notification payload verbatim so the browser plugin reads the same shape.
func (a *Agent) handleNotification(line []byte, env *droidEnvelope) {
	var params droidNotification
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad notification params", "agent_id", a.AgentID(), "error", err)
		return
	}
	payload := params.Notification
	if len(payload) == 0 {
		// Some notifications carry the payload at the top level of params.
		payload = env.Params
	}
	var head droidNotifHead
	if err := json.Unmarshal(payload, &head); err != nil {
		slog.Debug("droid: bad notification payload", "agent_id", a.AgentID(), "error", err)
		return
	}

	switch head.Type {
	case contracts.DroidNotificationWorkingStateChanged:
		a.onWorkingStateChanged(payload)
	case contracts.DroidNotificationCreateMessage:
		a.onCreateMessage(payload)
	case contracts.DroidNotificationAssistantTextDelta:
		a.onAssistantTextDelta(payload)
	case contracts.DroidNotificationAssistantTextComplete:
		a.onAssistantTextComplete(payload)
	case contracts.DroidToolNotificationToolCall:
		a.onToolCall(payload)
	case contracts.DroidToolNotificationToolResult:
		a.onToolResult(payload)
	case contracts.DroidNotificationAgentTurnCompleted:
		// A turn end for a CHILD session disarms that child's flag, not the
		// main turn. The notification envelope names the session.
		if params.SessionID != "" && params.SessionID != a.mainSessionID() {
			a.disarmChildTurn(params.SessionID)
			a.persistNotification(payload)
			return
		}
		a.onAgentTurnCompleted(payload)
	case contracts.DroidNotificationSettingsUpdated:
		a.onSettingsUpdated(payload)
	case contracts.DroidNotificationSessionTitleUpdated:
		a.persistNotification(payload)
	case contracts.DroidNotificationSessionTokenUsageChanged:
		a.persistNotification(payload)
	case contracts.DroidNotificationError:
		a.onError(payload)
	case droidNotificationChildSessionAvailable:
		a.onChildSessionAvailable(payload)
	default:
		// An unknown notification type must move nothing. Persist it so the
		// browser can still draw it, and leave the turn flag alone.
		a.persistNotification(payload)
	}
}

// persistNotification stores a notification row verbatim.
func (a *Agent) persistNotification(payload []byte) {
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, payload); err != nil {
		slog.Debug("droid: persist notification failed", "agent_id", a.AgentID(), "error", err)
	}
}

// persistRow stores one transcript row verbatim.
func (a *Agent) persistRow(payload []byte, span agent.SpanInfo) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload}, span); err != nil {
		slog.Debug("droid: persist message failed", "agent_id", a.AgentID(), "error", err)
	}
}

// onChildSessionAvailable arms the child's turn and persists the announcement.
// The child starts running as soon as its parent's Task tool spawns it, so the
// announcement marks the turn active.
func (a *Agent) onChildSessionAvailable(payload []byte) {
	var n struct {
		Type           string `json:"type"`
		ChildSessionID string `json:"childSessionId"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		slog.Debug("droid: bad child_session_available", "agent_id", a.AgentID(), "error", err)
		return
	}
	if n.ChildSessionID != "" {
		a.armChildTurn(n.ChildSessionID)
	}
	a.persistNotification(payload)
}

// mainSessionID returns the session id of the main conversation.
func (a *Agent) mainSessionID() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.sessionID
}

// droidWorkingState is the payload of droid_working_state_changed.
type droidWorkingState struct {
	Type     string `json:"type"`
	NewState string `json:"newState"`
}

// onWorkingStateChanged moves the turn flag on the named states only. Every
// other frame must move nothing.
func (a *Agent) onWorkingStateChanged(payload []byte) {
	var n droidWorkingState
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	switch n.NewState {
	case contracts.DroidWorkingStateThinking, contracts.DroidWorkingStateStreamingAssistantMessage,
		contracts.DroidWorkingStateExecutingTool, contracts.DroidWorkingStateWaitingForToolConfirmation,
		contracts.DroidWorkingStateCompactingConversation:
		a.armTurn()
	case contracts.DroidWorkingStateIdle:
		a.disarmTurn()
	}
}

// onCreateMessage records a new message and opens a span for it.
func (a *Agent) onCreateMessage(payload []byte) {
	var n struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	var msg struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(n.Message, &msg); err != nil {
		return
	}
	spanID := "droid-msg-" + msg.ID
	a.Mu.Lock()
	if a.messageSpans == nil {
		a.messageSpans = make(map[string]string)
	}
	a.messageSpans[msg.ID] = spanID
	a.Mu.Unlock()
	// The message envelope is the transcript row; the browser draws it.
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID})
}

// droidTextDelta is the payload of assistant_text_delta.
type droidTextDelta struct {
	Type       string `json:"type"`
	MessageID  string `json:"messageId"`
	BlockIndex int    `json:"blockIndex"`
	TextDelta  string `json:"textDelta"`
}

// onAssistantTextDelta accumulates streamed assistant text.
func (a *Agent) onAssistantTextDelta(payload []byte) {
	var n droidTextDelta
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	a.Mu.Lock()
	spanID := a.messageSpans[n.MessageID]
	a.Mu.Unlock()
	if spanID == "" {
		spanID = "droid-msg-" + n.MessageID
	}
	a.generation.Append(spanID, agent.AssembledMessageKindText, n.TextDelta, providerkit.JoinVerbatim)
}

// onAssistantTextComplete flushes the streamed text into a transcript row.
func (a *Agent) onAssistantTextComplete(payload []byte) {
	var n struct {
		Type      string `json:"type"`
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	a.Mu.Lock()
	spanID := a.messageSpans[n.MessageID]
	a.Mu.Unlock()
	if spanID == "" {
		spanID = "droid-msg-" + n.MessageID
	}
	raw, ok, err := a.generation.Finish(spanID, agent.MessageCompletionComplete)
	if err != nil || !ok {
		return
	}
	a.persistRow(raw, agent.SpanInfo{SpanID: spanID})
}

// droidToolUse is the tool_use record of a tool_call notification.
type droidToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// onToolCall opens a tool span.
func (a *Agent) onToolCall(payload []byte) {
	var n struct {
		Type    string       `json:"type"`
		ToolUse droidToolUse `json:"toolUse"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	spanID := "droid-tool-" + n.ToolUse.ID
	a.Mu.Lock()
	if a.tools == nil {
		a.tools = make(map[string]*droidTool)
	}
	a.tools[n.ToolUse.ID] = &droidTool{
		id:     n.ToolUse.ID,
		name:   n.ToolUse.Name,
		spanID: spanID,
		input:  n.ToolUse.Input,
	}
	a.Mu.Unlock()
	a.sink.OpenSpan(spanID, "")
	a.sink.SetSpanType(spanID, n.ToolUse.Name)
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, SpanType: n.ToolUse.Name})
}

// droidToolResult is the payload of tool_result.
type droidToolResult struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"toolUseId"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"isError"`
}

// onToolResult closes a tool span.
func (a *Agent) onToolResult(payload []byte) {
	var n droidToolResult
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	a.Mu.Lock()
	tool := a.tools[n.ToolUseID]
	delete(a.tools, n.ToolUseID)
	a.turnToolUse++
	uses := a.turnToolUse
	a.Mu.Unlock()
	spanID := "droid-tool-" + n.ToolUseID
	if tool != nil {
		spanID = tool.spanID
	}
	a.sink.CloseSpan(spanID)
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, Closing: true})
	_ = uses
}

// onAgentTurnCompleted ends the turn. PersistTurnEnd runs before the clear, so
// the turn's tool count reaches the activity latch first.
func (a *Agent) onAgentTurnCompleted(payload []byte) {
	a.Mu.Lock()
	uses := a.turnToolUse
	a.turnToolUse = 0
	a.Mu.Unlock()

	content := agent.MessageContent{Original: payload}
	if uses > 0 {
		content = agent.WithToolUseCount(content, uses)
	}
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Debug("droid: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
	a.disarmTurn()
}

// droidSettingsUpdated is the payload of settings_updated.
type droidSettingsUpdated struct {
	Type     string `json:"type"`
	Settings struct {
		ModelID         string `json:"modelId"`
		ReasoningEffort string `json:"reasoningEffort"`
		AutonomyMode    string `json:"autonomyMode"`
	} `json:"settings"`
}

// onSettingsUpdated folds the live configuration back into the agent state.
func (a *Agent) onSettingsUpdated(payload []byte) {
	var n droidSettingsUpdated
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	a.Mu.Lock()
	if n.Settings.ModelID != "" {
		a.settings.model = n.Settings.ModelID
	}
	if n.Settings.ReasoningEffort != "" {
		a.settings.reasoningEffort = n.Settings.ReasoningEffort
	}
	if n.Settings.AutonomyMode != "" {
		a.settings.autonomyMode = n.Settings.AutonomyMode
		if mode := droidModeForAutonomy(n.Settings.AutonomyMode); mode != "" {
			a.settings.permissionMode = mode
		}
	}
	a.Mu.Unlock()
	a.persistNotification(payload)
}

// onError surfaces a CLI error as a notification row and ends the turn when one
// runs.
func (a *Agent) onError(payload []byte) {
	a.persistNotification(payload)
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if active {
		a.disarmTurn()
	}
}

// adoptSessionID records the session id that initialize_session returned, and
// publishes it. A session opened with a resume handle already knows it; the
// response confirms the same id and must not replace it with a new one.
func (a *Agent) adoptSessionID(env *droidEnvelope) {
	var result struct {
		SessionID string `json:"sessionId"`
		Settings  struct {
			ModelID         string `json:"modelId"`
			ReasoningEffort string `json:"reasoningEffort"`
			AutonomyMode    string `json:"autonomyMode"`
		} `json:"settings"`
		AvailableModels []struct {
			ID                        string   `json:"id"`
			DisplayName               string   `json:"displayName"`
			SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
		} `json:"availableModels"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		slog.Debug("droid: initialize_session result unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	models := make([]droidModel, 0, len(result.AvailableModels))
	for _, m := range result.AvailableModels {
		models = append(models, droidModel{id: m.ID, displayName: m.DisplayName, efforts: m.SupportedReasoningEfforts})
	}
	a.Mu.Lock()
	if a.sessionID == "" {
		a.sessionID = result.SessionID
	}
	sessionID := a.sessionID
	if result.Settings.ModelID != "" {
		a.settings.model = result.Settings.ModelID
	}
	if result.Settings.ReasoningEffort != "" {
		a.settings.reasoningEffort = result.Settings.ReasoningEffort
	}
	if result.Settings.AutonomyMode != "" {
		a.settings.autonomyMode = result.Settings.AutonomyMode
		if mode := droidModeForAutonomy(result.Settings.AutonomyMode); mode != "" {
			a.settings.permissionMode = mode
		}
	}
	if len(models) > 0 {
		a.catalog.models = models
	}
	a.Mu.Unlock()
	a.sink.UpdateSessionID(sessionID)
}
