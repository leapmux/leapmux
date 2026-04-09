package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ACP JSON-RPC method name constants shared across all ACP providers.
const (
	acpMethodInitialize               = "initialize"
	acpMethodSessionUpdate            = "session/update"
	acpMethodSessionRequestPermission = "session/request_permission"
	acpMethodSessionCancel            = "session/cancel"
	acpMethodSessionNew               = "session/new"
	acpMethodSessionLoad              = "session/load"
	acpMethodSessionPrompt            = "session/prompt"
	acpMethodSessionSetModel          = "session/set_model"
	acpMethodSessionSetMode           = "session/set_mode"
)

// ACP session update type constants.
const (
	acpUpdateAgentMessageChunk       = "agent_message_chunk"
	acpUpdateAgentThoughtChunk       = "agent_thought_chunk"
	acpUpdateToolCall                = "tool_call"
	acpUpdateToolCallUpdate          = "tool_call_update"
	acpUpdatePlan                    = "plan"
	acpUpdateUsageUpdate             = "usage_update"
	acpUpdateUserMessageChunk        = "user_message_chunk"
	acpUpdateAvailableCommandsUpdate = "available_commands_update"
	acpUpdateCurrentModeUpdate       = "current_mode_update"
	acpUpdateConfigOptionUpdate      = "config_option_update"
)

// acpPendingInput holds a user message queued while a prompt is in flight.
type acpPendingInput struct {
	content     string
	attachments []*leapmuxv1.Attachment
}

// jsonrpcBase extends processBase with JSON-RPC request/response plumbing
// shared by all ACP agents (GeminiCLIAgent, OpenCodeAgent) and CodexAgent.
type jsonrpcBase struct {
	processBase
	nextReqID   atomic.Int64
	pendingReqs sync.Map // reqID (int64) -> chan json.RawMessage

	// Prompt queueing: ACP servers support only one active prompt per session.
	// Messages arriving mid-turn are queued and coalesced into a single prompt
	// once the current turn completes.
	promptActive    bool              // true while a prompt RPC is in flight
	pendingMessages []acpPendingInput // queued messages waiting for current turn

	// promptFunc is set by the concrete agent during construction. It sends
	// a single prompt RPC and blocks until the turn completes (including
	// response handling). Called by runPrompt on a goroutine.
	promptFunc func(content string, attachments []*leapmuxv1.Attachment)
}

// acpBase extends jsonrpcBase with fields and methods shared by all ACP
// agents (GeminiCLIAgent, OpenCodeAgent, CopilotCLIAgent) but not CodexAgent.
type acpBase struct {
	jsonrpcBase
	sink               OutputSink
	providerName       string                  // e.g. "copilot", "gemini", "opencode" — used in log messages
	extraSessionUpdate acpSessionUpdateHandler // optional provider-specific session update handler
	extraMethod        acpMethodHandler        // optional provider-specific request/notification handler
	reapplySettings    func()                  // called by ClearContext after session/new to re-apply model, mode, etc.
	sessionID          string
	workingDir         string
	model              string
	permissionMode     string
	availableModels    []*leapmuxv1.AvailableModel
	availableModes     []*leapmuxv1.AvailableOption
	turnAssistantText  strings.Builder
	turnThinkingText   strings.Builder
}

// handleACPPromptResponse extracts accumulated turn text, calls the optional
// prePersist hook, persists the prompt response, and resets the tool-use count.
func (b *acpBase) handleACPPromptResponse(resp json.RawMessage, prePersist func(json.RawMessage)) {
	if resp == nil {
		return
	}

	b.mu.Lock()
	thinkingText := b.turnThinkingText.String()
	b.turnThinkingText.Reset()
	assistantText := b.turnAssistantText.String()
	b.turnAssistantText.Reset()
	b.turnToolUses = 0
	b.mu.Unlock()

	if prePersist != nil {
		prePersist(resp)
	}

	b.persistPromptResponse(thinkingText, assistantText, resp, func(resp json.RawMessage) json.RawMessage {
		return b.enrichWithToolUses(resp)
	})
}

// acpSessionUpdateHandler is called for session update types not handled by
// the shared dispatcher. Return true if the update was consumed.
type acpSessionUpdateHandler func(sessionUpdate string, update json.RawMessage) bool

func configOptionSessionUpdateHandler(handler func(json.RawMessage)) acpSessionUpdateHandler {
	return func(sessionUpdate string, update json.RawMessage) bool {
		if sessionUpdate == acpUpdateConfigOptionUpdate {
			handler(update)
			return true
		}
		return false
	}
}

// acpMethodHandler is called for JSON-RPC methods not handled by the shared
// ACP dispatcher. Return true if the method was consumed.
type acpMethodHandler func(line *parsedLine) bool

// handleACPSessionUpdate dispatches ACP sessionUpdate notifications by type.
func (b *acpBase) handleACPSessionUpdate(params json.RawMessage, extra acpSessionUpdateHandler) {
	var wrapper struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("acp session update unmarshal wrapper failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if len(wrapper.Update) == 0 {
		return
	}
	update := wrapper.Update

	var header struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Role          string          `json:"role"`
		Content       json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp session update unmarshal header failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}

	if header.Role == "result" {
		return
	}

	switch header.SessionUpdate {
	case acpUpdateAgentMessageChunk:
		b.broadcastACPChunk(header.Content, &b.turnAssistantText, acpUpdateAgentMessageChunk)
	case acpUpdateAgentThoughtChunk:
		b.broadcastACPChunk(header.Content, &b.turnThinkingText, acpUpdateAgentThoughtChunk)
	case acpUpdateToolCall:
		b.handleToolCall(update)
	case acpUpdateToolCallUpdate:
		b.handleToolCallUpdate(update)
	case acpUpdatePlan:
		b.handlePlan(update)
	case acpUpdateUsageUpdate:
		b.handleUsageUpdate(update)
	case acpUpdateUserMessageChunk, acpUpdateAvailableCommandsUpdate:
		// No-op: user_message_chunk is history replay; available_commands_update is informational.
	default:
		if extra != nil && extra(header.SessionUpdate, update) {
			return
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{}); err != nil {
			slog.Error("persist unknown acp sessionUpdate", "agent_id", b.agentID, "type", header.SessionUpdate, "error", err)
		}
	}
}

// ClearContext sends a session/new request on the running ACP process,
// replacing the current session with a fresh one. After the session is
// created, the reapplySettings callback (if set) re-applies provider-
// specific settings such as model and permission mode.
func (b *acpBase) ClearContext() (string, bool) {
	_, params := buildACPSessionRequest("", b.workingDir, acpMethodSessionNew, "")
	resp, err := b.sendRequest(acpMethodSessionNew, json.RawMessage(params), b.APITimeout())
	if err != nil {
		slog.Error("acp ClearContext failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", false
	}
	if err := jsonRPCResultError(resp); err != nil {
		slog.Error("acp ClearContext: RPC error", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", false
	}

	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &session); err != nil || session.SessionID == "" {
		slog.Error("acp ClearContext: invalid response", "provider", b.providerName, "agent_id", b.agentID, "error", err, "response", string(resp))
		return "", false
	}

	b.mu.Lock()
	b.sessionID = session.SessionID
	b.turnAssistantText.Reset()
	b.turnThinkingText.Reset()
	b.mu.Unlock()

	b.sink.UpdateSessionID(session.SessionID)

	if b.reapplySettings != nil {
		b.reapplySettings()
	}
	return session.SessionID, true
}

// reapplyModelAndPermissionMode re-applies the current model and permission
// mode after a session/new. Used as the default reapplySettings callback by
// agents that track permission mode (Copilot, Gemini, Goose).
func (b *acpBase) reapplyModelAndPermissionMode() {
	b.mu.Lock()
	model, mode := b.model, b.permissionMode
	b.mu.Unlock()
	acpApplySetting(b.providerName, b.agentID, "model", model, b.setModel)
	acpApplySetting(b.providerName, b.agentID, "mode", mode, b.setPermissionMode)
}

// setPermissionMode sends a session/set_mode RPC and updates the local field.
func (b *acpBase) setPermissionMode(mode string) error {
	b.mu.Lock()
	available := b.availableModes
	b.mu.Unlock()

	if err := b.acpSetMode(mode, available); err != nil {
		return err
	}
	b.mu.Lock()
	b.permissionMode = mode
	b.mu.Unlock()
	return nil
}

// CurrentSettings returns the current model and permission mode.
// Concrete types that track additional settings (e.g. primaryAgent)
// should override this method.
func (b *acpBase) CurrentSettings() *leapmuxv1.AgentSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          b.model,
		PermissionMode: b.permissionMode,
	}
}

// UpdateSettings applies model and permission-mode changes to a running
// ACP agent. Concrete types that use different settings (e.g. primaryAgent
// instead of permissionMode) should override this method.
func (b *acpBase) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	return acpApplySetting(b.providerName, b.agentID, "model", s.GetModel(), b.setModel) &&
		acpApplySetting(b.providerName, b.agentID, "mode", s.GetPermissionMode(), b.setPermissionMode)
}

// acpApplySetting applies a single setting, logging a warning and returning
// false on failure. Skips empty values.
func acpApplySetting(providerName, agentID, name, value string, apply func(string) error) bool {
	if value == "" {
		return true
	}
	if err := apply(value); err != nil {
		slog.Warn("failed to apply "+name, "provider", providerName, "agent_id", agentID, "error", err)
		return false
	}
	return true
}

// buildACPSessionRequest builds a newSession or loadSession JSON-RPC request.
func buildACPSessionRequest(resumeSessionID, workingDir, newMethod, resumeMethod string) (method string, params []byte) {
	p := map[string]interface{}{
		"cwd":        workingDir,
		"mcpServers": []interface{}{},
	}
	method = newMethod
	if resumeSessionID != "" {
		p["sessionId"] = resumeSessionID
		method = resumeMethod
	}
	params, err := json.Marshal(p)
	if err != nil {
		slog.Warn("acp session request marshal failed", "error", err)
	}
	return method, params
}

// jsonrpcMessage is a typed struct for serializing JSON-RPC requests and
// notifications. ID is omitted for notifications (IDs start at 1 via
// nextReqID.Add(1), so 0 is safely treated as "absent").
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponseMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func (b *jsonrpcBase) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := b.nextReqID.Add(1)

	ch := make(chan json.RawMessage, 1)
	b.pendingReqs.Store(reqID, ch)
	defer b.pendingReqs.Delete(reqID)

	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := b.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-b.processDone:
		return nil, b.processExitError()
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for %s response", method)
	}
}

func (b *jsonrpcBase) sendNotification(method string, params json.RawMessage) error {
	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if _, err := b.stdin.Write(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

func (b *jsonrpcBase) sendResponse(id json.RawMessage, result any) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (b *jsonrpcBase) sendErrorResponse(id json.RawMessage, code int, message string) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

func (b *jsonrpcBase) writeJSONRPCResponse(resp jsonrpcResponseMessage) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	data = append(data, '\n')
	if _, err := b.stdin.Write(data); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

// handleJSONRPCResponse checks if a parsed line is a JSON-RPC response and
// routes it to the pending request channel. Returns true if the line was consumed.
func (b *jsonrpcBase) handleJSONRPCResponse(line *parsedLine) bool {
	if line.ID == nil || line.Method != "" {
		return false
	}

	reqID, err := line.ID.Int64()
	if err != nil {
		return false
	}

	val, ok := b.pendingReqs.Load(reqID)
	if !ok {
		return false
	}

	ch := val.(chan json.RawMessage)
	if len(line.Error) > 0 && string(line.Error) != "null" {
		ch <- line.Error
	} else {
		ch <- line.Result
	}

	return true
}

// readOutputLoop reads JSONL lines from stdout, using handleJSONRPCResponse as
// the interceptor and forwarding remaining lines to the given output handler.
func (b *jsonrpcBase) readOutputLoop(scanner *bufio.Scanner, handle outputHandler) {
	b.readOutput(scanner, b.handleJSONRPCResponse, handle)
}

// enqueueOrSendPrompt queues a message if a prompt is in flight, or starts
// a new prompt goroutine if idle. Returns an error only if the agent is stopped.
func (b *jsonrpcBase) enqueueOrSendPrompt(content string, attachments []*leapmuxv1.Attachment) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		b.pendingMessages = append(b.pendingMessages, acpPendingInput{content, attachments})
		b.mu.Unlock()
		return nil
	}
	b.promptActive = true
	b.mu.Unlock()

	go b.runPrompt(content, attachments)
	return nil
}

// runPrompt calls the agent's promptFunc and then drains any queued messages.
// It runs on a dedicated goroutine and loops until the queue is empty.
func (b *jsonrpcBase) runPrompt(content string, attachments []*leapmuxv1.Attachment) {
	for {
		b.promptFunc(content, attachments)

		b.mu.Lock()
		if len(b.pendingMessages) == 0 || b.stopped {
			b.promptActive = false
			b.pendingMessages = nil
			b.mu.Unlock()
			return
		}
		pending := b.pendingMessages
		b.pendingMessages = nil
		b.mu.Unlock()

		// Coalesce queued messages into a single prompt.
		var parts []string
		var allAttachments []*leapmuxv1.Attachment
		for _, p := range pending {
			if p.content != "" {
				parts = append(parts, p.content)
			}
			allAttachments = append(allAttachments, p.attachments...)
		}
		content = strings.Join(parts, "\n\n")
		attachments = allAttachments
	}
}

// SendInput queues a user message or starts a new prompt if idle.
func (b *acpBase) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	b.mu.Lock()
	if b.sessionID == "" {
		b.mu.Unlock()
		return fmt.Errorf("agent has no active session")
	}
	b.mu.Unlock()
	return b.enqueueOrSendPrompt(content, attachments)
}

// Stop clears the prompt queue and terminates the process.
func (b *acpBase) Stop() {
	b.clearPromptQueue()
	b.processBase.Stop()
}

// clearPromptQueue discards any queued messages and resets the prompt-active flag.
func (b *jsonrpcBase) clearPromptQueue() {
	b.mu.Lock()
	b.pendingMessages = nil
	b.promptActive = false
	b.mu.Unlock()
}

// sendPrompt builds and sends an ACP prompt, then calls the provided
// response handler. Shared by all ACP agent doSendPrompt implementations.
func (b *acpBase) sendPrompt(
	content string,
	attachments []*leapmuxv1.Attachment,
	sendRPC func(json.RawMessage) (json.RawMessage, error),
	handleResponse func(json.RawMessage),
) {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	prompt := buildACPPromptBlocks(content, classifyAttachments(attachments))
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	})
	if err != nil {
		slog.Warn("acp marshal prompt params", "agent_id", b.agentID, "error", err)
		return
	}

	resp, err := sendRPC(json.RawMessage(params))
	if err != nil {
		if !b.IsStopped() {
			slog.Error("acp prompt failed", "agent_id", b.agentID, "error", err)
			b.sink.BroadcastNotification(map[string]interface{}{
				"type":  "agent_error",
				"error": fmt.Sprintf("prompt failed: %v", err),
			})
		}
		return
	}
	handleResponse(resp)
}

// broadcastACPChunk extracts text from a pre-parsed content RawMessage and
// broadcasts it. The content JSON is already extracted from the header parse,
// avoiding a full re-unmarshal of the update on the streaming hot path.
func (b *acpBase) broadcastACPChunk(content json.RawMessage, builder *strings.Builder, eventType string) {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &c); err != nil {
		slog.Warn("acp broadcast chunk unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if c.Text == "" {
		return
	}
	b.mu.Lock()
	builder.WriteString(c.Text)
	b.mu.Unlock()
	b.sink.BroadcastStreamChunk([]byte(c.Text), "", eventType)
}

func (b *acpBase) persistTextMessage(sessionUpdate, text string) {
	if text == "" {
		return
	}

	msgContent, err := json.Marshal(map[string]interface{}{
		"sessionUpdate": sessionUpdate,
		"content": map[string]interface{}{
			"type": "text",
			"text": text,
		},
	})
	if err != nil {
		slog.Warn("marshal acp text content", "agent_id", b.agentID, "error", err)
		return
	}
	if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msgContent, SpanInfo{}); err != nil {
		slog.Error("persist acp text", "agent_id", b.agentID, "session_update", sessionUpdate, "error", err)
	}
}

func (b *acpBase) persistPromptResponse(
	thinkingText, assistantText string,
	resp json.RawMessage,
	enrich func(json.RawMessage) json.RawMessage,
) {
	b.persistTextMessage(acpUpdateAgentThoughtChunk, thinkingText)
	b.persistTextMessage(acpUpdateAgentMessageChunk, assistantText)

	resp = unwrapACPResult(resp)
	if enrich != nil {
		resp = enrich(resp)
	}
	if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, resp, SpanInfo{}); err != nil {
		slog.Error("persist acp prompt result", "agent_id", b.agentID, "error", err)
	}
	b.sink.ResetSpans()
}

// unwrapACPResult extracts the inner content from an ACP result message.
// Some ACP server versions return session/prompt results wrapped in:
//
//	{id, role: "result", seq, created_at, content: {stopReason, usage, ...}}
//
// The frontend classifier expects stopReason at the top level, so we unwrap
// the content field. This is a no-op when the response is not wrapped.
func unwrapACPResult(resp json.RawMessage) json.RawMessage {
	var wrapper struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(resp, &wrapper); err != nil {
		slog.Warn("acp unwrap result unmarshal failed", "error", err)
		return resp
	}
	if wrapper.Role != "result" || len(wrapper.Content) == 0 {
		return resp
	}
	return wrapper.Content
}

func (b *acpBase) handleToolCall(update json.RawMessage) {
	var tc struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(update, &tc); err != nil {
		slog.Warn("acp tool_call unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if tc.ToolCallID == "" {
		return
	}

	spanType := tc.Kind
	if spanType == "" {
		spanType = acpUpdateToolCall
	}

	// Tool calls that arrive already terminal (completed/failed/cancelled)
	// are persisted as closing spans immediately — no open/close cycle.
	if tc.Status == "completed" || tc.Status == "failed" || tc.Status == "cancelled" {
		if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
			SpanID: tc.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist terminal acp tool_call", "agent_id", b.agentID, "kind", tc.Kind, "status", tc.Status, "error", err)
		}
		return
	}

	spanColor := b.sink.PeekNextSpanColor()
	if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
		SpanID: tc.ToolCallID, SpanType: spanType, SpanColor: spanColor,
	}); err != nil {
		slog.Error("persist acp tool_call", "agent_id", b.agentID, "kind", tc.Kind, "error", err)
	}
	b.sink.SetSpanType(tc.ToolCallID, spanType)
	b.sink.OpenSpan(tc.ToolCallID, "")
}

func (b *acpBase) handleToolCallUpdate(update json.RawMessage) {
	var tcu struct {
		ToolCallID string `json:"toolCallId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(update, &tcu); err != nil {
		slog.Warn("acp tool_call_update unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if tcu.ToolCallID == "" {
		return
	}

	switch tcu.Status {
	case "in_progress":
		b.sink.BroadcastStreamChunk(update, tcu.ToolCallID, acpUpdateToolCallUpdate)
	case "completed", "failed", "cancelled":
		b.mu.Lock()
		b.turnToolUses++
		b.mu.Unlock()

		spanType := b.sink.GetSpanType(tcu.ToolCallID)
		if spanType == "" {
			spanType = acpUpdateToolCall
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
			SpanID: tcu.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist acp tool_call_update", "agent_id", b.agentID, "status", tcu.Status, "error", err)
		}
		b.sink.BroadcastStreamEnd(tcu.ToolCallID)
		b.sink.CloseSpan(tcu.ToolCallID)
	}
}

func (b *acpBase) handleUsageUpdate(update json.RawMessage) {
	var usage struct {
		Used int64 `json:"used"`
		Size int64 `json:"size"`
		Cost struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(update, &usage); err != nil {
		slog.Warn("acp usage update unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}

	info := map[string]interface{}{
		"contextUsage": map[string]interface{}{
			"inputTokens":              usage.Used,
			"cacheCreationInputTokens": int64(0),
			"cacheReadInputTokens":     int64(0),
			"outputTokens":             int64(0),
			"contextWindow":            usage.Size,
		},
	}
	if usage.Cost.Amount > 0 {
		info["totalCostUsd"] = usage.Cost.Amount
	}
	b.sink.BroadcastSessionInfo(info)
}

// parseJSONRPCError extracts the code and message from a JSON-RPC error
// response. Returns ok=false if resp is empty, null, or not an error object.
func parseJSONRPCError(resp json.RawMessage) (code int, message string, ok bool) {
	if len(resp) == 0 || string(resp) == "null" {
		return 0, "", false
	}
	var rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return 0, "", false
	}
	if rpcErr.Message == "" {
		return 0, "", false
	}
	return rpcErr.Code, rpcErr.Message, true
}

// jsonRPCResultError returns an error if resp is a JSON-RPC error response.
func jsonRPCResultError(resp json.RawMessage) error {
	code, message, ok := parseJSONRPCError(resp)
	if !ok {
		return nil
	}
	return fmt.Errorf("json-rpc error %d: %s", code, message)
}

// ExtractJSONRPCID extracts the JSON-RPC "id" field from a raw JSON payload,
// returning the raw bytes, its string representation, and whether extraction succeeded.
func ExtractJSONRPCID(content []byte) (json.RawMessage, string, bool) {
	var payload struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(content, &payload); err != nil {
		slog.Warn("json-rpc id unmarshal failed", "error", err)
		return nil, "", false
	}
	if len(payload.ID) == 0 || string(payload.ID) == "null" {
		return nil, "", false
	}

	var text string
	if json.Unmarshal(payload.ID, &text) == nil {
		return payload.ID, text, true
	}

	text = strings.TrimSpace(string(payload.ID))
	if text == "" {
		return nil, "", false
	}
	return payload.ID, text, true
}

// acpSessionConfig describes the session methods for a specific ACP provider.
type acpSessionConfig struct {
	newMethod    string // e.g. "session/new"
	resumeMethod string // e.g. "session/load" or "session/resume"
}

// acpDefaultSessionConfig is the standard ACP session config used by most providers.
var acpDefaultSessionConfig = acpSessionConfig{
	newMethod:    acpMethodSessionNew,
	resumeMethod: acpMethodSessionLoad,
}

// acpSessionResult holds the parsed result of the ACP session handshake.
type acpSessionResult struct {
	SessionID      string
	CurrentModelID string
	Models         []acpModelInfo
	CurrentModeID  string
	Modes          []acpModeInfo
	ConfigOptions  []acpConfigOption
	Raw            json.RawMessage // full session response for provider-specific parsing
}

// startACPHandshake performs the common ACP startup handshake: stderr drain,
// scanner setup, initialize request, session request with resume-fallback,
// session ID validation, and UpdateSessionID/BroadcastStatusActive.
func (b *acpBase) startACPHandshake(
	stdout, stderr io.ReadCloser,
	opts Options,
	initParams json.RawMessage,
	sessionCfg acpSessionConfig,
) (*acpSessionResult, error) {
	b.drainStderr(stderr)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go b.readOutputLoop(scanner, b.handleOutput)

	cleanup := func() {
		b.Stop()
		_ = b.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	initResp, err := b.sendRequest(acpMethodInitialize, initParams, timeout)
	if err != nil {
		cleanup()
		return nil, b.formatStartupError("initialize", err)
	}
	if err := jsonRPCResultError(initResp); err != nil {
		cleanup()
		return nil, b.formatStartupError("initialize", err)
	}

	// 2. Send session request (resume or new).
	sessionMethod, sessionParams := buildACPSessionRequest(opts.ResumeSessionID, opts.WorkingDir, sessionCfg.newMethod, sessionCfg.resumeMethod)
	sessionResp, err := b.sendRequest(sessionMethod, json.RawMessage(sessionParams), timeout)
	if err != nil {
		if opts.ResumeSessionID != "" {
			slog.Warn("session resume failed, falling back to new session",
				"agent_id", b.agentID, "session_id", opts.ResumeSessionID, "error", err)
			_, fallbackParams := buildACPSessionRequest("", opts.WorkingDir, sessionCfg.newMethod, sessionCfg.resumeMethod)
			sessionResp, err = b.sendRequest(sessionCfg.newMethod, json.RawMessage(fallbackParams), timeout)
		}
		if err != nil {
			cleanup()
			return nil, b.formatStartupError(sessionMethod, err)
		}
	}
	if err := jsonRPCResultError(sessionResp); err != nil {
		cleanup()
		return nil, b.formatStartupError(sessionMethod, err)
	}

	// 3. Parse the common session fields.
	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string         `json:"currentModelId"`
			AvailableModels []acpModelInfo `json:"availableModels"`
		} `json:"models"`
		Modes *struct {
			CurrentModeID  string        `json:"currentModeId"`
			AvailableModes []acpModeInfo `json:"availableModes"`
		} `json:"modes"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(sessionResp, &session); err != nil {
		cleanup()
		return nil, b.formatStartupError("session parse", err)
	}
	if session.SessionID == "" && opts.ResumeSessionID != "" && sessionMethod == sessionCfg.resumeMethod {
		session.SessionID = opts.ResumeSessionID
	}
	if session.SessionID == "" {
		cleanup()
		return nil, b.formatStartupError(sessionMethod, fmt.Errorf("response did not contain a session ID"))
	}

	b.sessionID = session.SessionID
	b.workingDir = opts.WorkingDir
	b.sink.UpdateSessionID(b.sessionID)
	b.sink.BroadcastStatusActive(b.sessionID)

	result := &acpSessionResult{
		SessionID:      session.SessionID,
		CurrentModelID: session.Models.CurrentModelID,
		Models:         session.Models.AvailableModels,
		ConfigOptions:  session.ConfigOptions,
		Raw:            sessionResp,
	}
	if session.Modes != nil {
		result.CurrentModeID = session.Modes.CurrentModeID
		result.Modes = session.Modes.AvailableModes
	}

	return result, nil
}

// acpModeInfo is the JSON shape shared by all ACP providers for mode metadata.
type acpModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpModelInfo is the JSON shape shared by all ACP providers for model metadata.
type acpModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type acpConfigOption struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	CurrentValue string                 `json:"currentValue"`
	Options      []acpConfigOptionValue `json:"options"`
}

type acpConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// buildACPModels converts a list of acpModelInfo into proto AvailableModel messages.
// If normalize is non-nil, it is applied to each model ID (and the currentModelID) before use.
func buildACPModels(models []acpModelInfo, currentModelID string, normalize func(string) string) []*leapmuxv1.AvailableModel {
	if normalize != nil {
		currentModelID = normalize(currentModelID)
	}
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
	for _, m := range models {
		id := m.ModelID
		if normalize != nil {
			id = normalize(id)
		}
		if id == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = id
		}
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          id,
			DisplayName: name,
			Description: m.Description,
			IsDefault:   id == currentModelID,
		})
	}
	return result
}

// buildACPModes converts a list of acpModeInfo into proto AvailableOption messages.
// If filter is non-nil, modes for which filter returns true are skipped.
func buildACPModes(modes []acpModeInfo, currentModeID string, filter func(id string) bool) []*leapmuxv1.AvailableOption {
	result := make([]*leapmuxv1.AvailableOption, 0, len(modes))
	for _, mode := range modes {
		if mode.ID == "" {
			continue
		}
		if filter != nil && filter(mode.ID) {
			continue
		}
		name := titleCaseID(mode.ID, mode.Name)
		result = append(result, &leapmuxv1.AvailableOption{
			Id:          mode.ID,
			Name:        name,
			Description: mode.Description,
			IsDefault:   mode.ID == currentModeID,
		})
	}
	return result
}

func parseACPConfigOptions(raw json.RawMessage) []acpConfigOption {
	var payload struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("acp config options unmarshal failed", "error", err)
		return nil
	}
	return payload.ConfigOptions
}

func syncACPConfigOptions(
	model *string,
	mode *string,
	availableModels *[]*leapmuxv1.AvailableModel,
	availableModes *[]*leapmuxv1.AvailableOption,
	options []acpConfigOption,
	normalize func(configID, value string) string,
) string {
	if len(options) == 0 {
		return ""
	}
	if normalize == nil {
		normalize = func(_ string, value string) string { return value }
	}

	var updatedMode string
	for _, option := range options {
		switch option.ID {
		case "model":
			currentValue := normalize(option.ID, option.CurrentValue)
			if currentValue != "" {
				*model = currentValue
			}
			if len(option.Options) > 0 {
				models := make([]*leapmuxv1.AvailableModel, 0, len(option.Options))
				for _, candidate := range option.Options {
					value := normalize(option.ID, candidate.Value)
					if value == "" {
						continue
					}
					name := candidate.Name
					if name == "" {
						name = value
					}
					models = append(models, &leapmuxv1.AvailableModel{
						Id:          value,
						DisplayName: name,
						Description: candidate.Description,
						IsDefault:   value == currentValue,
					})
				}
				if len(models) > 0 {
					*availableModels = models
				}
			}
		case "mode":
			currentValue := normalize(option.ID, option.CurrentValue)
			if currentValue != "" {
				*mode = currentValue
				updatedMode = currentValue
			}
			if len(option.Options) > 0 {
				modes := make([]*leapmuxv1.AvailableOption, 0, len(option.Options))
				for _, candidate := range option.Options {
					value := normalize(option.ID, candidate.Value)
					if value == "" {
						continue
					}
					name := titleCaseID(value, candidate.Name)
					modes = append(modes, &leapmuxv1.AvailableOption{
						Id:          value,
						Name:        name,
						Description: candidate.Description,
						IsDefault:   value == currentValue,
					})
				}
				if len(modes) > 0 {
					*availableModes = modes
				}
			}
		}
	}
	return updatedMode
}

// AvailableModels returns the models reported by the ACP provider.
func (b *acpBase) AvailableModels() []*leapmuxv1.AvailableModel {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.availableModels
}

// setModel sends a session/set_model request and updates the local model field.
func (b *acpBase) setModel(model string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modelId":   model,
	})
	if err != nil {
		return fmt.Errorf("marshal setModel params: %w", err)
	}
	resp, err := b.sendRequest(acpMethodSessionSetModel, json.RawMessage(params), b.APITimeout())
	if err != nil {
		return err
	}
	if err := jsonRPCResultError(resp); err != nil {
		return err
	}
	b.mu.Lock()
	b.model = model
	b.mu.Unlock()
	return nil
}

// acpSetMode sends a session/set_mode request and returns nil on success.
// If available is non-empty and modeID is not found, an error is returned.
func (b *acpBase) acpSetMode(modeID string, available []*leapmuxv1.AvailableOption) error {
	if len(available) > 0 && !hasACPOption(available, modeID) {
		return fmt.Errorf("unknown mode: %s", modeID)
	}

	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    modeID,
	})
	if err != nil {
		return fmt.Errorf("marshal setMode params: %w", err)
	}
	resp, err := b.sendRequest(acpMethodSessionSetMode, json.RawMessage(params), b.APITimeout())
	if err != nil {
		return err
	}
	return jsonRPCResultError(resp)
}

// cancelSession sends a session/cancel notification.
func (b *acpBase) cancelSession() error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
	if err != nil {
		return fmt.Errorf("marshal cancel params: %w", err)
	}
	return b.sendNotification(acpMethodSessionCancel, json.RawMessage(params))
}

// capitalizeFirst returns s with its first rune upper-cased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		return string(unicode.ToUpper(r)) + s[len(string(r)):]
	}
	return s
}

// titleCaseID returns name if it is a distinct display name (non-empty and
// different from id). Otherwise it title-cases the id by splitting on
// underscores or hyphens, capitalizing each word, and joining with spaces
// (e.g. "smart_approve" → "Smart Approve", "full-auto" → "Full Auto").
func titleCaseID(id, name string) string {
	if name != "" && name != id {
		return name
	}
	if id == "" {
		return ""
	}
	// Determine separator: prefer underscore, fall back to hyphen.
	sep := "_"
	if !strings.Contains(id, "_") && strings.Contains(id, "-") {
		sep = "-"
	}
	parts := strings.Split(id, sep)
	for i, p := range parts {
		parts[i] = capitalizeFirst(p)
	}
	return strings.Join(parts, " ")
}

// hasACPOption returns true if any option in the slice has the given id.
func hasACPOption(options []*leapmuxv1.AvailableOption, id string) bool {
	if id == "" {
		return false
	}
	for _, option := range options {
		if option != nil && option.Id == id {
			return true
		}
	}
	return false
}

func (b *acpBase) handlePlan(update json.RawMessage) {
	if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{}); err != nil {
		slog.Error("persist acp plan", "agent_id", b.agentID, "error", err)
	}
}

func (b *acpBase) handleRequestPermission(id *json.Number, content []byte) {
	if id == nil {
		slog.Warn("acp requestPermission missing id", "agent_id", b.agentID)
		return
	}
	requestID := id.String()
	b.sink.PersistControlRequest(requestID, content)
	b.sink.BroadcastControlRequest(requestID, content)
}

// handleOutput dispatches a single parsed output line using the provider's
// extraSessionUpdate handler. Used as the outputHandler for readOutputLoop.
func (b *acpBase) handleOutput(line *parsedLine) {
	slog.Debug("acp HandleOutput", "provider", b.providerName, "agent_id", b.agentID, "method", line.Method, "len", len(line.Raw))
	b.handleACPOutput(line, b.extraSessionUpdate, b.extraMethod)
}

// HandleOutput processes a single JSONL notification from an ACP provider.
func (b *acpBase) HandleOutput(content []byte) {
	b.handleOutput(parseLine(content))
}

// handleACPOutput is the shared output dispatcher for all ACP providers.
// It routes session updates and permission requests, persisting anything else.
func (b *acpBase) handleACPOutput(line *parsedLine, extraSessionUpdate acpSessionUpdateHandler, extraMethod acpMethodHandler) {
	switch line.Method {
	case acpMethodSessionUpdate:
		b.handleACPSessionUpdate(line.Params, extraSessionUpdate)
	case acpMethodSessionRequestPermission:
		b.handleRequestPermission(line.ID, line.Raw)
	default:
		if extraMethod != nil && extraMethod(line) {
			return
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("acp persist notification", "agent_id", b.agentID, "method", line.Method, "error", err)
		}
	}
}

// doSendACPPrompt sends a single ACP prompt RPC and processes the response.
// Used as the promptFunc for all ACP agents; handleResponse varies per provider.
func (b *acpBase) doSendACPPrompt(content string, attachments []*leapmuxv1.Attachment, handleResponse func(json.RawMessage)) {
	b.sendPrompt(content, attachments,
		func(params json.RawMessage) (json.RawMessage, error) {
			return b.sendRequest(acpMethodSessionPrompt, params, 10*time.Minute)
		},
		handleResponse,
	)
}
