package letta

import (
	"cmp"
	"encoding/json"
	"slices"

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
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	a.Mu.Lock()
	if delta.ToolCallID != "" && a.closedSpans.WasClosed(toolKey) {
		a.Mu.Unlock()
		return
	}
	tool := a.tools[toolKey]
	owned := tool != nil && delta.RunID != "" && tool.runID == delta.RunID && !tool.ended()
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
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	a.Mu.Lock()
	if delta.ToolCallID != "" && a.closedSpans.WasClosed(toolKey) {
		a.Mu.Unlock()
		return
	}
	tool := a.tools[toolKey]
	owned := tool != nil && tool.runID == delta.RunID && valid &&
		(delta.Status == contracts.LettaToolStatusSuccess || delta.Status == contracts.LettaToolStatusError)
	if owned {
		tool.endStatus = delta.Status
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

// finishToolOutputScope ends the live output of each call that one scope leaves
// open, and stores no row for it.
//
// A child task ends its calls this way. A native result of a child can arrive
// after its task ends (see toolOutputTarget), and a closing row before it would
// give the call a second result.
func (a *Agent) finishToolOutputScope(subagentID string, target agent.ProviderServices) {
	for _, tool := range a.takeScopeTools(subagentID) {
		target.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		target.CloseSpan(tool.spanID)
	}
}

// closeToolScope ends each call that one scope leaves open, and stores the row
// that opened the call again as its closing row. The completion of that row
// states how the call ended (see lettaTool.closingCompletion). Letta Code sends
// no result for such a call, so without the closing row the call never ends in
// the transcript. The copy holds only bytes that Letta Code sent.
//
// A turn of the main conversation and the process each end their calls this
// way. Letta Code sends the result of each call that an interrupt stops before
// it sends `turn_finished`, and a process that ended sends nothing.
func (a *Agent) closeToolScope(subagentID string, target agent.ProviderServices, completion agent.MessageCompletion) {
	discarding := a.IsDiscardingOutput()
	tools := a.takeScopeTools(subagentID)
	if len(tools) > 0 {
		a.Mu.Lock()
		for _, tool := range tools {
			a.closedSpans.MarkClosed(lettaToolKey(tool.subagentID, tool.id))
		}
		a.Mu.Unlock()
	}
	for _, tool := range tools {
		target.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		if !discarding {
			a.persistContentTo(target, agent.MessageContent{Original: tool.row, Completion: tool.closingCompletion(completion)},
				agent.SpanInfo{SpanID: tool.spanID, SpanType: tool.name, Closing: true})
		}
		target.CloseSpan(tool.spanID)
	}
}

// takeScopeTools removes the open calls of one scope, and returns them in the
// order in which they opened.
func (a *Agent) takeScopeTools(subagentID string) []*lettaTool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var tools []*lettaTool
	for key, tool := range a.tools {
		if tool.subagentID == subagentID {
			tools = append(tools, tool)
			delete(a.tools, key)
		}
	}
	slices.SortFunc(tools, func(left, right *lettaTool) int {
		return cmp.Compare(left.order, right.order)
	})
	return tools
}
