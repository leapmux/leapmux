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

// ACP JSON-RPC method name constants shared across ACP providers (Gemini CLI, OpenCode).
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

// ACP session update type constants shared across providers.
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
// agents (GeminiCLIAgent, OpenCodeAgent) but not CodexAgent.
type acpBase struct {
	jsonrpcBase
	sink              OutputSink
	sessionID         string
	turnAssistantText strings.Builder
	turnThinkingText  strings.Builder
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

// handleACPSessionUpdate dispatches ACP sessionUpdate notifications by type.
func (b *acpBase) handleACPSessionUpdate(params json.RawMessage, extra acpSessionUpdateHandler) {
	var wrapper struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrapper) != nil || len(wrapper.Update) == 0 {
		return
	}

	var header struct {
		SessionUpdate string `json:"sessionUpdate"`
		Role          string `json:"role"`
	}
	if json.Unmarshal(wrapper.Update, &header) != nil {
		return
	}

	if header.Role == "result" {
		return
	}

	switch header.SessionUpdate {
	case acpUpdateAgentMessageChunk:
		b.appendACPChunk(wrapper.Update, &b.turnAssistantText, acpUpdateAgentMessageChunk)
	case acpUpdateAgentThoughtChunk:
		b.appendACPChunk(wrapper.Update, &b.turnThinkingText, acpUpdateAgentThoughtChunk)
	case acpUpdateToolCall:
		b.handleToolCall(wrapper.Update)
	case acpUpdateToolCallUpdate:
		b.handleToolCallUpdate(wrapper.Update)
	case acpUpdatePlan:
		b.handlePlan(wrapper.Update)
	case acpUpdateUsageUpdate:
		b.handleUsageUpdate(wrapper.Update)
	case acpUpdateUserMessageChunk, acpUpdateAvailableCommandsUpdate:
		// No-op: user_message_chunk is history replay; available_commands_update is informational.
	default:
		if extra != nil && extra(header.SessionUpdate, wrapper.Update) {
			return
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, wrapper.Update, SpanInfo{}); err != nil {
			slog.Error("persist unknown acp sessionUpdate", "agent_id", b.agentID, "type", header.SessionUpdate, "error", err)
		}
	}
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
	params, _ = json.Marshal(p)
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

// sendRequest sends a JSON-RPC request and waits for the response.
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

// sendNotification sends a JSON-RPC notification (no id, no response expected).
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
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	})

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

func (b *acpBase) appendACPChunk(update json.RawMessage, builder *strings.Builder, eventType string) {
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &chunk) == nil && chunk.Content.Text != "" {
		b.mu.Lock()
		builder.WriteString(chunk.Content.Text)
		b.mu.Unlock()
		b.sink.BroadcastStreamChunk([]byte(chunk.Content.Text), "", eventType)
	}
}

func (b *acpBase) persistTextMessage(sessionUpdate, text string) {
	if text == "" {
		return
	}

	msgContent, _ := json.Marshal(map[string]interface{}{
		"sessionUpdate": sessionUpdate,
		"content": map[string]interface{}{
			"type": "text",
			"text": text,
		},
	})
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
	if json.Unmarshal(resp, &wrapper) != nil || wrapper.Role != "result" || len(wrapper.Content) == 0 {
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
	if json.Unmarshal(update, &tc) != nil || tc.ToolCallID == "" {
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
	if json.Unmarshal(update, &tcu) != nil || tcu.ToolCallID == "" {
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
	if json.Unmarshal(update, &usage) != nil {
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

// acpSessionConfig describes the session methods for a specific ACP provider.
type acpSessionConfig struct {
	newMethod    string // e.g. "session/new"
	resumeMethod string // e.g. "session/load" or "session/resume"
}

// acpSessionResult holds the parsed result of the ACP session handshake.
type acpSessionResult struct {
	SessionID      string
	CurrentModelID string
	Models         []acpModelInfo
	CurrentModeID  string
	Modes          []acpModeInfo
	Raw            json.RawMessage // full session response for provider-specific parsing
}

// startACPHandshake performs the common ACP startup handshake: stderr drain,
// scanner setup, initialize request, session request with resume-fallback,
// session ID validation, and UpdateSessionID/BroadcastStatusActive.
func (b *acpBase) startACPHandshake(
	stdout, stderr io.ReadCloser,
	handleOutput outputHandler,
	opts Options,
	initParams json.RawMessage,
	sessionCfg acpSessionConfig,
) (*acpSessionResult, error) {
	b.drainStderr(stderr)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go b.readOutputLoop(scanner, handleOutput)

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
	b.sink.UpdateSessionID(b.sessionID)
	b.sink.BroadcastStatusActive(b.sessionID)

	result := &acpSessionResult{
		SessionID:      session.SessionID,
		CurrentModelID: session.Models.CurrentModelID,
		Models:         session.Models.AvailableModels,
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

// buildACPModels converts a list of acpModelInfo into proto AvailableModel messages.
func buildACPModels(models []acpModelInfo, currentModelID string) []*leapmuxv1.AvailableModel {
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
	for _, m := range models {
		if m.ModelID == "" {
			continue
		}
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          m.ModelID,
			DisplayName: m.Name,
			Description: m.Description,
			IsDefault:   m.ModelID == currentModelID,
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
		name := mode.Name
		if name == "" {
			name = capitalizeFirst(mode.ID)
		}
		result = append(result, &leapmuxv1.AvailableOption{
			Id:          mode.ID,
			Name:        name,
			Description: mode.Description,
			IsDefault:   mode.ID == currentModeID,
		})
	}
	return result
}

// acpSetModel sends a session/set_model request and returns nil on success.
func (b *acpBase) acpSetModel(model string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modelId":   model,
	})
	resp, err := b.sendRequest(acpMethodSessionSetModel, json.RawMessage(params), 10*time.Second)
	if err != nil {
		return err
	}
	return jsonRPCResultError(resp)
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

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    modeID,
	})
	resp, err := b.sendRequest(acpMethodSessionSetMode, json.RawMessage(params), 10*time.Second)
	if err != nil {
		return err
	}
	return jsonRPCResultError(resp)
}

// acpCancelSession sends a session/cancel notification.
func (b *acpBase) acpCancelSession() error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
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
