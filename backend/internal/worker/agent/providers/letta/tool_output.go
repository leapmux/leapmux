package letta

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type lettaNativeToolReturn struct {
	CallID string          `json:"tool_call_id"`
	Status string          `json:"status"`
	Return json.RawMessage `json:"tool_return"`
}

// isLettaToolProgress recognizes the exact ID that the native output emitter uses.
func isLettaToolProgress(delta *lettaDelta) bool {
	return delta.MessageType == contracts.LettaDeltaKindToolReturnMessage && delta.ToolCallID != "" &&
		delta.ID == contracts.LettaToolOutputStreamIDPrefix+delta.ToolCallID
}

// lettaReturnedData distinguishes absence from empty, zero, false, and null output.
func lettaReturnedData(delta *lettaDelta) (json.RawMessage, bool, bool) {
	if len(delta.ToolReturns) == 0 {
		return delta.ToolReturn, len(delta.ToolReturn) != 0, true
	}
	var returns []lettaNativeToolReturn
	if json.Unmarshal(delta.ToolReturns, &returns) != nil || returns == nil || delta.ToolCallID == "" {
		return nil, false, false
	}
	var selected *lettaNativeToolReturn
	for i := range returns {
		result := &returns[i]
		if result.CallID != delta.ToolCallID {
			continue
		}
		if selected != nil {
			return nil, false, false
		}
		selected = result
	}
	if selected == nil || (delta.Status != "" && selected.Status != "" && delta.Status != selected.Status) {
		return nil, false, false
	}
	if len(delta.ToolReturn) != 0 {
		if len(selected.Return) != 0 && !agent.JSONCanonicalEqual(delta.ToolReturn, selected.Return) {
			return nil, false, false
		}
		return delta.ToolReturn, true, true
	}
	return selected.Return, len(selected.Return) != 0, true
}

// Late native results still belong to a registered child after its task ends.
func (a *Agent) toolOutputTarget(subagentID, messageType string) (agent.ProviderServices, bool) {
	if target, ok := a.outputTarget(subagentID); ok {
		return target, true
	}
	if messageType != contracts.LettaDeltaKindToolReturnMessage && messageType != contracts.LettaDeltaKindClientToolEnd {
		return nil, false
	}
	child := a.children[subagentID]
	if child == nil || !child.registered {
		return nil, false
	}
	return a.sink.ChildSink(child.agentID), true
}

func (a *Agent) onToolProgress(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	a.Mu.Lock()
	tool := a.tools[lettaToolKey(subagentID, delta.ToolCallID)]
	owned := tool != nil && delta.RunID != "" && tool.runID == delta.RunID && !tool.executionEnded
	a.Mu.Unlock()
	span := agent.SpanInfo{}
	if owned {
		span.SpanID = tool.spanID
		span.SpanType = tool.name
	}
	a.persistRowTo(target, payload, span)
	output, present, valid := lettaReturnedData(delta)
	var text string
	if !owned || !present || !valid || delta.Status != contracts.LettaToolStatusSuccess || json.Unmarshal(output, &text) != nil {
		return
	}
	// Each event contains a current normalized window. It supplies no byte count or truncation flag.
	target.ReportProgress(agent.OutputTailProgress(tool.spanID, text, false))
}

func (a *Agent) onClientToolEnd(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	_, present, valid := lettaReturnedData(delta)
	if present && valid {
		a.onToolReturn(payload, delta, target, subagentID)
		return
	}
	a.Mu.Lock()
	tool := a.tools[lettaToolKey(subagentID, delta.ToolCallID)]
	owned := tool != nil && tool.runID == delta.RunID && valid &&
		(delta.Status == contracts.LettaToolStatusSuccess || delta.Status == contracts.LettaToolStatusError)
	if owned {
		tool.executionEnded = true
	}
	a.Mu.Unlock()
	span := agent.SpanInfo{}
	if owned {
		span.SpanID = tool.spanID
		span.SpanType = tool.name
		target.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
	}
	a.persistRowTo(target, payload, span)
}

// Native scope completion clears live state without creating a result message.
func (a *Agent) finishToolOutputScope(subagentID string, target agent.ProviderServices) {
	a.Mu.Lock()
	var tools []*lettaTool
	for key, tool := range a.tools {
		if tool.subagentID == subagentID {
			tools = append(tools, tool)
			delete(a.tools, key)
		}
	}
	a.Mu.Unlock()
	for _, tool := range tools {
		target.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		target.CloseSpan(tool.spanID)
	}
}
