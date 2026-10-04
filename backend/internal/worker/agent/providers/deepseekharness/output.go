package deepseekharness

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type assistantMessageEventData struct {
	Message struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	} `json:"message"`
	Interrupted bool `json:"interrupted"`
}

// handleSessionEvent preserves the original event bytes and native block order.
func (a *Agent) handleSessionEvent(stream *sessionStream, raw []byte) error {
	var event sessionEvent
	if err := json.Unmarshal(raw, &event); err != nil || event.Type == "" || event.Seq == nil || *event.Seq < 0 || *event.Seq > maxSessionSequence || len(event.Data) == 0 {
		return fmt.Errorf("DeepSeek Harness sent an invalid Session event")
	}
	if *event.Seq <= stream.lastSeq {
		return nil
	}
	if a.discard.Load() {
		stream.lastSeq = *event.Seq
		return nil
	}
	if err := a.applySessionEvent(stream, event, raw); err != nil {
		return err
	}
	stream.lastSeq = *event.Seq
	return nil
}

func (a *Agent) applySessionEvent(stream *sessionStream, event sessionEvent, raw []byte) error {
	sink := a.streamSink(stream)
	content := agent.MessageContent{Original: raw, AgentSessionID: stream.sessionID, IdempotencyKey: fmt.Sprintf("session-event:%d", *event.Seq)}
	switch event.Type {
	case contracts.DeepseekHarnessEventTurnStart:
		stream.tools = 0
		stream.startedAt = event.Time
		if stream.childAgentID == "" {
			a.setTurnState(true)
		} else {
			if err := a.childTurnState(stream.sessionID, true, agent.MessageCompletionComplete); err != nil {
				return err
			}
		}
	case contracts.DeepseekHarnessEventAssistantMessage:
		var data assistantMessageEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		completion := agent.MessageCompletionComplete
		if data.Interrupted {
			completion = agent.MessageCompletionInterrupted
		}
		for index, block := range data.Message.Content {
			if block.Type == contracts.DeepseekHarnessContentTypeToolCall {
				continue
			}
			if block.Type != contracts.DeepseekHarnessContentTypeText && block.Type != contracts.DeepseekHarnessContentTypeReasoning {
				continue
			}
			supplemental, err := encodeJSON(map[string]any{contracts.DeepseekHarnessSupplementBlockIndex: index})
			if err != nil {
				return err
			}
			blockContent := content
			blockContent.IdempotencyKey += fmt.Sprintf(":block:%d", index)
			blockContent.Supplemental = supplemental
			blockContent.Completion = completion
			if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, blockContent, agent.SpanInfo{}); err != nil {
				return err
			}
		}
	case contracts.DeepseekHarnessEventUserMessage:
		if stream.childAgentID != "" {
			return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, agent.SpanInfo{})
		}
	case contracts.DeepseekHarnessEventToolCall:
		var call struct {
			ID   string `json:"callId"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(event.Data, &call); err != nil || call.ID == "" || call.Name == "" {
			return fmt.Errorf("DeepSeek Harness tool call has no identity")
		}
		sink.OpenSpan(call.ID, "")
		sink.SetSpanType(call.ID, call.Name)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: call.ID, SpanType: call.Name, SpanColor: sink.ReserveSpanColor(call.ID, "")}); err != nil {
			return err
		}
		stream.tools++
		stream.pendingOrder = append(stream.pendingOrder, call.ID)
		stream.pending[call.ID] = append([]byte(nil), raw...)
	case contracts.DeepseekHarnessEventToolResult:
		var result struct {
			Message struct {
				ID      string `json:"toolCallId"`
				IsError bool   `json:"isError"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &result); err != nil || result.Message.ID == "" {
			return fmt.Errorf("DeepSeek Harness tool result has no call identity")
		}
		id := result.Message.ID
		completion := agent.MessageCompletionComplete
		if result.Message.IsError {
			completion = agent.MessageCompletionError
		}
		content.Completion = completion
		err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: id, SpanType: sink.GetSpanType(id), SpanColor: sink.ReserveSpanColor(id, ""), Closing: true})
		if err != nil {
			return err
		}
		if err := a.bindChildResult(stream, id, event.Data); err != nil {
			return err
		}
		delete(stream.pending, id)
		sink.CloseSpan(id)
	case contracts.DeepseekHarnessEventTurnEnd:
		completion := nativeTurnCompletion(event.Data)
		if err := a.finishPendingTools(stream, completion); err != nil {
			return err
		}
		metadata, err := encodeJSON(map[string]any{contracts.MessageMetadataFieldDurationMs: max(int64(0), event.Time-stream.startedAt)})
		if err != nil {
			return err
		}
		content.Metadata = metadata
		content.Completion = completion
		content = agent.WithToolUseCount(content, int(stream.tools))
		if err := sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
			return err
		}
		if stream.childAgentID == "" {
			a.setTurnState(false)
		} else {
			if err := a.childTurnState(stream.sessionID, false, completion); err != nil {
				return err
			}
		}
	case contracts.DeepseekHarnessEventCompactionEnd, contracts.DeepseekHarnessEventRequestContext:
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	case contracts.DeepseekHarnessEventPlanMode:
		if stream.childAgentID == "" {
			var state struct {
				Active bool `json:"active"`
			}
			if err := json.Unmarshal(event.Data, &state); err != nil {
				return err
			}
			mode := contracts.DeepseekHarnessModeAct
			if state.Active {
				mode = contracts.DeepseekHarnessModePlan
			}
			a.Mu.Lock()
			a.mode = mode
			a.Mu.Unlock()
			a.sink.UpdatePermissionMode(mode)
		}
	case contracts.DeepseekHarnessEventGoalChange:
		if stream.childAgentID == "" {
			return a.applyGoalEvent(event.Data, false)
		}
	case contracts.DeepseekHarnessEventSubagentCatalog:
		return a.beginChild(stream, event.Data)
	case contracts.DeepseekHarnessEventWorkflowStart, contracts.DeepseekHarnessEventWorkflowEnd, contracts.DeepseekHarnessEventWorkflowAgentStart, contracts.DeepseekHarnessEventWorkflowAgentEnd:
		return a.workflowEvent(stream, event.Type, event.Data)
	case contracts.DeepseekHarnessEventTodoWrite:
		content.Completion = agent.MessageCompletionComplete
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	}
	return nil
}

func nativeTurnCompletion(raw []byte) agent.MessageCompletion {
	var data struct {
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return agent.MessageCompletionError
	}
	switch data.Reason.Kind {
	case "completed", "max-tokens":
		return agent.MessageCompletionComplete
	case "interrupted", "aborted":
		return agent.MessageCompletionInterrupted
	default:
		return agent.MessageCompletionError
	}
}

func (a *Agent) finishPendingTools(stream *sessionStream, completion agent.MessageCompletion) error {
	sink := a.streamSink(stream)
	for _, id := range stream.pendingOrder {
		raw, exists := stream.pending[id]
		if !exists {
			continue
		}
		var event sessionEvent
		if err := json.Unmarshal(raw, &event); err != nil || event.Seq == nil {
			return fmt.Errorf("DeepSeek Harness pending tool has no native sequence")
		}
		content := agent.MessageContent{Original: raw, AgentSessionID: stream.sessionID, Completion: completion, IdempotencyKey: fmt.Sprintf("session-event:%d:retained-close", *event.Seq)}
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: id, SpanType: sink.GetSpanType(id), SpanColor: sink.ReserveSpanColor(id, ""), Closing: true}); err != nil {
			return err
		}
		sink.CloseSpan(id)
		delete(stream.pending, id)
	}
	stream.pendingOrder = nil
	return nil
}

// Live stream text changes only the generation counter. The browser reads saved native blocks.
func (a *Agent) handleAssistantStream(stream *sessionStream, raw []byte) error {
	var frame struct {
		Type      string `json:"type"`
		AttemptID string `json:"attemptId"`
		Chunk     struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Arguments string `json:"argumentsDelta"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return err
	}
	if frame.Type != "chunk" {
		return nil
	}
	text := frame.Chunk.Text
	if text == "" {
		text = frame.Chunk.Arguments
	}
	if text != "" {
		a.streamSink(stream).ReportProgress(agent.ModelTextProgress(frame.AttemptID, text))
	}
	return nil
}
