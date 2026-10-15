package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// droidAttachmentLabel identifies the provider in an attachment refusal.
const droidAttachmentLabel = "Factory Droid"

// Droid emits thinking blocks separately from assistant text blocks. The
// worker assembles both before the browser reads them.
const (
	droidNotificationThinkingTextDelta    = "thinking_text_delta"
	droidNotificationThinkingTextComplete = "thinking_text_complete"
)

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

	childSessionID := a.dispatchFrame(line, &env)
	if childSessionID != "" {
		a.startChildTail(childSessionID)
	}
}

// dispatchFrame keeps native event state in order. A child archive starts after
// this method releases dispatchMu because its reader can dispatch more events.
func (a *Agent) dispatchFrame(line []byte, env *droidEnvelope) string {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	switch env.Type {
	case droidTypeNotification:
		return a.handleNotification(env)
	case droidTypeRequest:
		a.handleServerRequest(line, env)
	case droidTypeResponse:
		// The startup response settles initialize_session or load_session.
		if env.ID == droidInitRequestID {
			if env.Error != nil {
				a.settleStartup(env.Error)
			} else {
				a.settleStartup(a.adoptSessionID(env))
			}
			return ""
		}
		if a.handleCompactionResponse(*env) {
			return ""
		}
		if a.settleReply(*env) {
			return ""
		}
		if env.Error != nil {
			slog.Debug("droid: request failed", "agent_id", a.AgentID(), "error", env.Error)
		}
	default:
		slog.Debug("droid: unknown envelope type", "agent_id", a.AgentID(), "type", env.Type)
	}
	return ""
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
func (a *Agent) handleNotification(env *droidEnvelope) string {
	var params droidNotification
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad notification params", "agent_id", a.AgentID(), "error", err)
		return ""
	}
	payload := params.Notification
	if len(payload) == 0 {
		// Some notifications carry the payload at the top level of params.
		payload = env.Params
	}
	var head droidNotifHead
	if err := json.Unmarshal(payload, &head); err != nil {
		slog.Debug("droid: bad notification payload", "agent_id", a.AgentID(), "error", err)
		return ""
	}
	if head.Type == droidNotificationChildSessionAvailable {
		return a.onChildSessionAvailable(params.SessionID, payload)
	}
	target, ok := a.outputTargetFor(params.SessionID)
	if !ok {
		slog.Debug("droid: notification from an unknown session", "agent_id", a.AgentID(), "session_id", params.SessionID)
		return ""
	}
	a.flushPendingOutput(target)
	defer a.releaseEndedOutput(target)

	switch head.Type {
	case contracts.DroidNotificationWorkingStateChanged:
		a.onWorkingStateChanged(payload, target.childSessionID)
	case contracts.DroidNotificationCreateMessage:
		a.onCreateMessage(payload, target)
	case contracts.DroidNotificationAssistantTextDelta:
		a.onModelTextDelta(payload, agent.AssembledMessageKindText, target)
	case contracts.DroidNotificationAssistantTextComplete:
		a.onModelTextComplete(payload, agent.AssembledMessageKindText, target)
	case droidNotificationThinkingTextDelta:
		a.onModelTextDelta(payload, agent.AssembledMessageKindReasoning, target)
	case droidNotificationThinkingTextComplete:
		a.onModelTextComplete(payload, agent.AssembledMessageKindReasoning, target)
	case contracts.DroidToolNotificationToolCall:
		a.onToolCall(payload, target)
	case contracts.DroidToolNotificationToolResult:
		a.onToolResult(payload, target)
	case contracts.DroidNotificationAgentTurnCompleted:
		a.onAgentTurnCompleted(payload, target)
	case contracts.DroidNotificationSettingsUpdated:
		if target.childSessionID == "" {
			a.onSettingsUpdated(payload)
		} else {
			a.persistNotification(payload, target)
		}
	case contracts.DroidNotificationSessionTitleUpdated:
		a.persistNotification(payload, target)
	case contracts.DroidNotificationSessionTokenUsageChanged:
		a.persistNotification(payload, target)
	case contracts.DroidNotificationSessionCompacted:
		a.persistNotification(payload, target)
		if target.childSessionID == "" {
			a.finishCompactionFromNotification()
			a.refreshContextUsage()
		}
	case contracts.DroidNotificationError:
		a.onError(payload, target)
	default:
		// An unknown notification type must move nothing. Persist it so the
		// browser can still draw it, and leave the turn flag alone.
		a.persistNotification(payload, target)
	}
	return ""
}

// persistNotification stores a notification row verbatim.
func (a *Agent) persistNotification(payload []byte, target droidOutputTarget) {
	a.enqueueOutput(target, agent.MessageContent{Original: payload}, agent.SpanInfo{}, droidOutputNotification)
}

// persistRow stores one transcript row verbatim.
func (a *Agent) persistRow(payload []byte, span agent.SpanInfo, target droidOutputTarget) {
	a.enqueueOutput(target, agent.MessageContent{Original: payload}, span, droidOutputMessage)
}

// onChildSessionAvailable registers a native child under its parent session.
func (a *Agent) onChildSessionAvailable(parentSessionID string, payload []byte) string {
	var n struct {
		Type           string `json:"type"`
		ChildSessionID string `json:"childSessionId"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		slog.Debug("droid: bad child_session_available", "agent_id", a.AgentID(), "error", err)
		return ""
	}
	childSessionID := a.registerChildSession(parentSessionID, payload)
	target, ok := a.outputTargetFor(parentSessionID)
	if ok {
		a.persistNotification(payload, target)
	}
	return childSessionID
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

// onWorkingStateChanged moves the turn flag on the listed states only. Every
// other frame must move nothing.
func (a *Agent) onWorkingStateChanged(payload []byte, childSessionID string) {
	var n droidWorkingState
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	switch n.NewState {
	case contracts.DroidWorkingStateThinking, contracts.DroidWorkingStateStreamingAssistantMessage,
		contracts.DroidWorkingStateExecutingTool, contracts.DroidWorkingStateWaitingForToolConfirmation,
		contracts.DroidWorkingStateCompactingConversation:
		if childSessionID != "" {
			a.armChildTurn(childSessionID)
		} else {
			a.armTurn()
		}
	case contracts.DroidWorkingStateIdle:
		if childSessionID != "" {
			a.disarmChildTurn(childSessionID)
		} else {
			a.disarmTurn()
		}
	}
}

// onCreateMessage records a new message and opens a span for it.
func (a *Agent) onCreateMessage(payload []byte, target droidOutputTarget) {
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
	target.state.ended = false
	spanID := "droid-msg-" + msg.ID
	if target.state.messageSpans == nil {
		target.state.messageSpans = make(map[string]string)
	}
	target.state.messageSpans[msg.ID] = spanID
	// The message envelope is the transcript row; the browser draws it.
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID}, target)
}

// droidTextDelta is the payload of assistant_text_delta.
type droidTextDelta struct {
	Type       string `json:"type"`
	MessageID  string `json:"messageId"`
	BlockIndex int    `json:"blockIndex"`
	TextDelta  string `json:"textDelta"`
}

// onModelTextDelta accumulates one text or thinking block.
func (a *Agent) onModelTextDelta(payload []byte, kind agent.AssembledMessageKind, target droidOutputTarget) {
	var n droidTextDelta
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	if n.MessageID == "" || n.TextDelta == "" {
		return
	}
	target.state.ended = false
	target.state.generation.Append(modelTextScope(target.state, n.MessageID, n.BlockIndex, kind), kind, n.TextDelta, providerkit.JoinVerbatim)
}

// modelTextScope keeps a thinking block separate from the answer that follows.
func modelTextScope(state *droidOutputState, messageID string, blockIndex int, kind agent.AssembledMessageKind) string {
	spanID := state.messageSpans[messageID]
	if spanID == "" {
		spanID = "droid-msg-" + messageID
	}
	if kind == agent.AssembledMessageKindReasoning {
		return spanID + ":thinking:" + strconv.Itoa(blockIndex)
	}
	return spanID
}

// onModelTextComplete flushes one complete text or thinking block.
func (a *Agent) onModelTextComplete(payload []byte, kind agent.AssembledMessageKind, target droidOutputTarget) {
	var n struct {
		Type       string `json:"type"`
		MessageID  string `json:"messageId"`
		BlockIndex int    `json:"blockIndex"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	if n.MessageID == "" {
		return
	}
	scope := modelTextScope(target.state, n.MessageID, n.BlockIndex, kind)
	_, err := target.state.generation.PersistScope(scope, agent.MessageCompletionComplete, func(raw []byte) error {
		a.persistRow(raw, agent.SpanInfo{SpanID: scope}, target)
		return nil
	})
	if err != nil {
		slog.Error("droid: persist completed text failed", "agent_id", a.AgentID(), "error", err)
	}
}

// droidToolUse is the tool_use record of a tool_call notification.
type droidToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// onToolCall opens a tool span.
func (a *Agent) onToolCall(payload []byte, target droidOutputTarget) {
	var n struct {
		Type    string       `json:"type"`
		ToolUse droidToolUse `json:"toolUse"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	target.state.ended = false
	spanID := "droid-tool-" + n.ToolUse.ID
	if target.state.tools == nil {
		target.state.tools = make(map[string]*droidTool)
	}
	target.state.tools[n.ToolUse.ID] = &droidTool{
		id:     n.ToolUse.ID,
		name:   n.ToolUse.Name,
		spanID: spanID,
		input:  n.ToolUse.Input,
	}
	sessionID := target.childSessionID
	if sessionID == "" {
		sessionID = a.mainSessionID()
	}
	a.rememberTaskSpawn(sessionID, n.ToolUse)
	target.sink.OpenSpan(spanID, "")
	target.sink.SetSpanType(spanID, n.ToolUse.Name)
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, SpanType: n.ToolUse.Name}, target)
}

// droidToolResult is the payload of tool_result.
type droidToolResult struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"toolUseId"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"isError"`
}

// onToolResult closes a tool span.
func (a *Agent) onToolResult(payload []byte, target droidOutputTarget) {
	var n droidToolResult
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	tool := target.state.tools[n.ToolUseID]
	delete(target.state.tools, n.ToolUseID)
	if tool != nil {
		target.state.turnToolUse++
	}
	if n.IsError {
		sessionID := target.childSessionID
		if sessionID == "" {
			sessionID = a.mainSessionID()
		}
		delete(a.childSpawns, droidSpawnKey(sessionID, n.ToolUseID))
	}
	spanID := "droid-tool-" + n.ToolUseID
	if tool != nil {
		spanID = tool.spanID
	}
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, Closing: true}, target)
	target.sink.CloseSpan(spanID)
}

// onAgentTurnCompleted ends the turn. PersistTurnEnd runs before the clear, so
// the turn's tool count reaches the activity latch first.
func (a *Agent) onAgentTurnCompleted(payload []byte, target droidOutputTarget) {
	var n struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	completion, status := droidTurnCompletion(n.Reason)
	a.persistRemainingGeneration(target, completion)
	a.persistTurnEnd(payload, target)
	if target.childSessionID != "" {
		a.closeChildSession(target.childSessionID, status)
	} else {
		a.disarmTurn()
		a.refreshContextUsage()
	}
}

// droidTurnCompletion follows the native child-result classification, including early process exits.
func droidTurnCompletion(reason string) (agent.MessageCompletion, bgtask.Status) {
	switch reason {
	case "", contracts.DroidTurnEndReasonCompleted, contracts.DroidTurnEndReasonSpecHandoff:
		return agent.MessageCompletionComplete, bgtask.StatusSucceeded
	case contracts.DroidTurnEndReasonCancelled, contracts.DroidTurnEndReasonProcessExit:
		return agent.MessageCompletionInterrupted, bgtask.StatusInterrupted
	case contracts.DroidTurnEndReasonError, contracts.DroidTurnEndReasonPermissionRejected,
		contracts.DroidTurnEndReasonStructuredOutputMissing, contracts.DroidTurnEndReasonStructuredOutputInvalid,
		contracts.DroidTurnEndReasonStructuredOutputSchemaInvalid, contracts.DroidTurnEndReasonModelUsageExhausted,
		contracts.DroidTurnEndReasonModelAuthenticationFailed, contracts.DroidTurnEndReasonModelRequestRejected,
		contracts.DroidTurnEndReasonModelProviderUnreachable, contracts.DroidTurnEndReasonModelProviderUnavailable,
		contracts.DroidTurnEndReasonModelRateLimited, contracts.DroidTurnEndReasonPromptRejected,
		contracts.DroidTurnEndReasonCompletionPersistenceFailed, contracts.DroidTurnEndReasonNoApproverAvailable:
		return agent.MessageCompletionError, bgtask.StatusFailed
	default:
		// An unknown native outcome cannot establish success.
		return agent.MessageCompletionError, bgtask.StatusFailed
	}
}

// persistRemainingGeneration saves blocks when Droid omits their completion events.
// The queue retains each failed write with its original completion and publication owner.
func (a *Agent) persistRemainingGeneration(target droidOutputTarget, completion agent.MessageCompletion) {
	err := target.state.generation.PersistAll(completion, func(raw []byte) error {
		a.persistRow(raw, agent.SpanInfo{}, target)
		return nil
	})
	if err != nil {
		slog.Error("droid: persist unfinished text failed", "agent_id", a.AgentID(), "error", err)
	}
}

// persistTurnEnd records one native outcome without deciding child lifecycle.
func (a *Agent) persistTurnEnd(payload []byte, target droidOutputTarget) {
	uses := target.state.turnToolUse
	target.state.turnToolUse = 0

	content := agent.WithToolUseCount(agent.MessageContent{Original: payload}, uses)
	a.enqueueOutput(target, content, agent.SpanInfo{}, droidOutputTurnEnd)
}

// onError surfaces a CLI error as a notification row and ends the turn when one
// runs.
func (a *Agent) onError(payload []byte, target droidOutputTarget) {
	a.persistRemainingGeneration(target, agent.MessageCompletionError)
	a.persistNotification(payload, target)
	if target.childSessionID != "" {
		a.closeChildSession(target.childSessionID, bgtask.StatusFailed)
		return
	}
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if active {
		a.disarmTurn()
	}
}

// settleStartup sends the result of the one native startup request to Start.
func (a *Agent) settleStartup(err error) {
	a.Mu.Lock()
	reply := a.startupReply
	a.Mu.Unlock()
	if reply != nil {
		select {
		case reply <- err:
		default:
		}
	}
}

// adoptSessionID records the new or loaded session identity and settings.
func (a *Agent) adoptSessionID(env *droidEnvelope) error {
	var result struct {
		SessionID       string              `json:"sessionId"`
		Settings        droidNativeSettings `json:"settings"`
		AvailableModels []struct {
			ID                        string   `json:"id"`
			DisplayName               string   `json:"displayName"`
			SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
		} `json:"availableModels"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fmt.Errorf("read droid startup result: %w", err)
	}
	models := make([]droidModel, 0, len(result.AvailableModels))
	for _, m := range result.AvailableModels {
		models = append(models, droidModel{id: m.ID, displayName: m.DisplayName, efforts: m.SupportedReasoningEfforts})
	}
	a.Mu.Lock()
	if a.sessionID != "" && result.SessionID != "" && a.sessionID != result.SessionID {
		expected := a.sessionID
		a.Mu.Unlock()
		return fmt.Errorf("droid startup returned session %q instead of %q", result.SessionID, expected)
	}
	if a.sessionID == "" {
		a.sessionID = result.SessionID
	}
	sessionID := a.sessionID
	if sessionID == "" {
		a.Mu.Unlock()
		return errors.New("droid startup returned no session id")
	}
	if result.Settings.ModelID != "" {
		a.settings.model = result.Settings.ModelID
	}
	if result.Settings.ReasoningEffort != "" {
		a.settings.reasoningEffort = result.Settings.ReasoningEffort
	}
	if result.Settings.InteractionMode != "" {
		a.settings.interactionMode = result.Settings.InteractionMode
	}
	if result.Settings.AutonomyLevel != "" {
		a.settings.autonomyLevel = result.Settings.AutonomyLevel
	}
	if mode := droidModeFromSettings(result.Settings); mode != "" {
		a.settings.permissionMode = mode
	}
	if len(models) > 0 {
		a.catalog.models = models
	}
	a.Mu.Unlock()
	a.sink.UpdateSessionID(sessionID)
	return nil
}
