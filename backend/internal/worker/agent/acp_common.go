package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"unicode"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ACP session update type constants shared across providers.
const (
	acpUpdateAgentMessageChunk    = "agent_message_chunk"
	acpUpdateAgentThoughtChunk    = "agent_thought_chunk"
	acpUpdateToolCall             = "tool_call"
	acpUpdateToolCallUpdate       = "tool_call_update"
	acpUpdatePlan                 = "plan"
	acpUpdateUsageUpdate          = "usage_update"
	acpUpdateUserMessageChunk     = "user_message_chunk"
	acpUpdateAvailableCommandsUpdate = "available_commands_update"
)

func sendACPRequest(
	stdin io.Writer,
	nextReqID *atomic.Int64,
	pendingReqs *sync.Map,
	processDone <-chan struct{},
	ctx context.Context,
	processExitError func() error,
	method string,
	params json.RawMessage,
	timeout time.Duration,
) (json.RawMessage, error) {
	reqID := nextReqID.Add(1)

	ch := make(chan json.RawMessage, 1)
	pendingReqs.Store(reqID, ch)
	defer pendingReqs.Delete(reqID)

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-processDone:
		return nil, processExitError()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for %s response", method)
	}
}

func sendACPNotification(stdin io.Writer, method string, params json.RawMessage) error {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

func handleACPJSONRPCResponse(pendingReqs *sync.Map, line []byte) bool {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}

	if envelope.ID == nil || envelope.Method != "" {
		return false
	}

	reqID, err := envelope.ID.Int64()
	if err != nil {
		return false
	}

	val, ok := pendingReqs.Load(reqID)
	if !ok {
		return false
	}

	ch := val.(chan json.RawMessage)
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		ch <- envelope.Error
	} else {
		ch <- envelope.Result
	}

	return true
}

func appendACPChunk(update json.RawMessage, builder *strings.Builder, mu *sync.Mutex, sink OutputSink, eventType string) {
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &chunk) == nil && chunk.Content.Text != "" {
		mu.Lock()
		builder.WriteString(chunk.Content.Text)
		mu.Unlock()
		sink.BroadcastStreamChunk([]byte(chunk.Content.Text), "", eventType)
	}
}

func persistACPTextMessage(agentID string, sink OutputSink, sessionUpdate, text string) {
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
	if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msgContent, SpanInfo{}); err != nil {
		slog.Error("persist acp text", "agent_id", agentID, "session_update", sessionUpdate, "error", err)
	}
}

func persistACPPromptResponse(
	agentID string,
	sink OutputSink,
	thinkingText, assistantText string,
	resp json.RawMessage,
	unwrap func(json.RawMessage) json.RawMessage,
	enrich func(json.RawMessage) json.RawMessage,
) {
	persistACPTextMessage(agentID, sink, "agent_thought_chunk", thinkingText)
	persistACPTextMessage(agentID, sink, "agent_message_chunk", assistantText)

	if unwrap != nil {
		resp = unwrap(resp)
	}
	if enrich != nil {
		resp = enrich(resp)
	}
	if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, resp, SpanInfo{}); err != nil {
		slog.Error("persist acp prompt result", "agent_id", agentID, "error", err)
	}
	sink.ResetSpans()
}

func handleACPToolCall(
	agentID string,
	sink OutputSink,
	update json.RawMessage,
	closeStatuses map[string]bool,
) {
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

	if closeStatuses[tc.Status] {
		if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
			SpanID: tc.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist terminal acp tool_call", "agent_id", agentID, "kind", tc.Kind, "status", tc.Status, "error", err)
		}
		return
	}

	spanColor := sink.PeekNextSpanColor()
	if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
		SpanID: tc.ToolCallID, SpanType: spanType, SpanColor: spanColor,
	}); err != nil {
		slog.Error("persist acp tool_call", "agent_id", agentID, "kind", tc.Kind, "error", err)
	}
	sink.SetSpanType(tc.ToolCallID, spanType)
	sink.OpenSpan(tc.ToolCallID, "")
}

func handleACPToolCallUpdate(
	agentID string,
	sink OutputSink,
	mu *sync.Mutex,
	turnToolUses *int,
	update json.RawMessage,
	terminalStatuses map[string]bool,
) {
	var tcu struct {
		ToolCallID string `json:"toolCallId"`
		Status     string `json:"status"`
	}
	if json.Unmarshal(update, &tcu) != nil || tcu.ToolCallID == "" {
		return
	}

	switch {
	case tcu.Status == "in_progress":
		sink.BroadcastStreamChunk(update, tcu.ToolCallID, acpUpdateToolCallUpdate)
	case terminalStatuses[tcu.Status]:
		mu.Lock()
		*turnToolUses = *turnToolUses + 1
		mu.Unlock()

		spanType := sink.GetSpanType(tcu.ToolCallID)
		if spanType == "" {
			spanType = acpUpdateToolCall
		}
		if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
			SpanID: tcu.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist acp tool_call_update", "agent_id", agentID, "status", tcu.Status, "error", err)
		}
		sink.BroadcastStreamEnd(tcu.ToolCallID)
		sink.CloseSpan(tcu.ToolCallID)
	}
}

func handleACPUsageUpdate(sink OutputSink, update json.RawMessage) {
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
	sink.BroadcastSessionInfo(info)
}

func broadcastGeminiQuotaSessionInfo(sink OutputSink, resp json.RawMessage) {
	var result struct {
		Meta struct {
			Quota struct {
				TokenCount struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"token_count"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	if json.Unmarshal(resp, &result) != nil {
		return
	}

	inputTokens := result.Meta.Quota.TokenCount.InputTokens
	outputTokens := result.Meta.Quota.TokenCount.OutputTokens
	if inputTokens == 0 && outputTokens == 0 {
		return
	}

	sink.BroadcastSessionInfo(map[string]interface{}{
		"contextUsage": map[string]interface{}{
			"inputTokens":              inputTokens,
			"cacheCreationInputTokens": int64(0),
			"cacheReadInputTokens":     int64(0),
			"outputTokens":             outputTokens,
		},
	})
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

func handleACPPlan(agentID string, sink OutputSink, update json.RawMessage) {
	if err := sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{}); err != nil {
		slog.Error("persist acp plan", "agent_id", agentID, "error", err)
	}
}
