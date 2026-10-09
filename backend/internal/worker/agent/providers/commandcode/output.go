package commandcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) messageContent(raw []byte) agent.MessageContent {
	a.Mu.Lock()
	id := a.sessionID
	a.Mu.Unlock()
	return agent.MessageContent{Original: raw, AgentSessionID: id}
}

func (a *Agent) persist(raw []byte, span agent.SpanInfo) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, a.messageContent(raw), span); err != nil {
		slog.Warn("persist a Command Code message", "error", err)
	}
}

func (a *Agent) handleMethod(line *providerkit.ParsedLine) {
	if line.HasID() {
		a.RefuseUnsupportedRequest(line)
		return
	}
	var envelope struct {
		Params struct {
			TurnID     string   `json:"turnId"`
			StopReason string   `json:"stopReason"`
			Leftover   []string `json:"leftover"`
		} `json:"params"`
	}
	if json.Unmarshal(line.Raw, &envelope) != nil {
		return
	}
	switch line.Method {
	case "turn/started":
		a.startTurn(envelope.Params.TurnID)
	case contracts.CommandCodeMethodTurnCompleted:
		a.completeTurn(line.Raw, envelope.Params.TurnID, envelope.Params.StopReason)
		for index, input := range envelope.Params.Leftover {
			if err := a.sink.RequeueDroppedInput(fmt.Sprintf("%s:%d", envelope.Params.TurnID, index), input, nil); err != nil {
				slog.Warn("restore undelivered Command Code steering", "error", err)
			}
		}
	case "protocol_error":
		_, _ = a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw})
	}
}

func (a *Agent) handleEvent(raw []byte) {
	var wrapper struct {
		Type  string      `json:"type"`
		Event nativeEvent `json:"event"`
	}
	if json.Unmarshal(raw, &wrapper) != nil || wrapper.Type != contracts.CommandCodeFrameKindEvent {
		return
	}
	event := wrapper.Event
	switch event.Type {
	case "text_delta", "thinking_delta":
		kind := agent.AssembledMessageKindText
		scope := "text"
		if event.Type == "thinking_delta" {
			kind, scope = agent.AssembledMessageKindReasoning, "thinking"
		}
		a.generation.Append(scope, kind, event.Delta, providerkit.JoinVerbatim)
		a.sink.ReportProgress(agent.ModelTextProgress(scope, event.Delta))
	case contracts.CommandCodeEventThinkingEnd:
		if event.Text != "" {
			a.persist(raw, agent.SpanInfo{})
		}
		a.generation.Discard("thinking")
	case contracts.CommandCodeEventMessageEnd:
		if nativeText(event.Content) != "" {
			a.persist(raw, agent.SpanInfo{})
		}
		a.generation.Discard("text")
		a.generation.Discard("thinking")
	case contracts.CommandCodeEventToolQueued:
		if event.ToolCallID == "" || event.ToolName == "" {
			return
		}
		a.Mu.Lock()
		if _, exists := a.tools[event.ToolCallID]; exists {
			a.Mu.Unlock()
			return
		}
		a.tools[event.ToolCallID] = openTool{name: event.ToolName, input: slices.Clone(event.Input), opening: slices.Clone(raw)}
		a.TurnToolUses++
		a.Mu.Unlock()
		a.persist(raw, agent.SpanInfo{SpanID: event.ToolCallID, SpanType: event.ToolName})
		a.sink.OpenSpan(event.ToolCallID, "")
		a.sink.SetSpanType(event.ToolCallID, event.ToolName)
	case contracts.CommandCodeEventToolUpdate:
		if event.ToolCallID == "" {
			return
		}
		a.Mu.Lock()
		_, known := a.tools[event.ToolCallID]
		a.Mu.Unlock()
		if !known {
			return
		}
		text := nativeText(event.Partial)
		a.sink.ReportProgress(agent.OutputDeltaProgress(event.ToolCallID, int64(len(text))))
		a.sink.ReportProgress(agent.OutputTailProgress(event.ToolCallID, text, false))
	case contracts.CommandCodeEventToolCompleted, contracts.CommandCodeEventToolErrored, contracts.CommandCodeEventToolDenied, contracts.CommandCodeEventToolHookBlocked:
		a.finishTool(raw, event)
	case contracts.CommandCodeEventModelRequestEnd:
		a.recordUsage(event.Usage)
	case contracts.CommandCodeEventCompactionStart:
		a.Mu.Lock()
		a.compacting = true
		a.Mu.Unlock()
		a.PublishTurnActive()
		_, _ = a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw})
	case contracts.CommandCodeEventCompactionDone:
		_, _ = a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw})
		a.Mu.Lock()
		a.compacting = false
		if event.Trigger == "manual" && a.manualCompaction != nil {
			a.manualCompaction.finish()
			a.manualCompaction = nil
		}
		a.Mu.Unlock()
		a.PublishTurnActive()
	case contracts.CommandCodeEventCompactionOutcome:
		a.Mu.Lock()
		if event.Trigger == "manual" && a.manualCompaction != nil {
			a.manualCompaction.outcome = event.Outcome
		}
		a.Mu.Unlock()
		_, _ = a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw})
	case contracts.CommandCodeEventNotice, contracts.CommandCodeEventApiRetry, contracts.CommandCodeEventRunError:
		_, _ = a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw})
	case contracts.CommandCodeEventSubagentStart, contracts.CommandCodeEventSubagentStop, contracts.CommandCodeEventSubagentProgress:
		a.observeChild(raw, event)
	case contracts.CommandCodeEventPermissionModeChanged:
		a.Mu.Lock()
		a.permissionMode = event.Mode
		a.Mu.Unlock()
		a.sink.UpdatePermissionMode(event.Mode)
	case contracts.CommandCodeEventConfigSettingChanged:
		// A later native state reply confirms the applied settings.
	case contracts.CommandCodeEventRunStart, contracts.CommandCodeEventRunEnd:
		// Native run_end repeats the whole session. turn/completed owns the boundary.
	}
}

func (a *Agent) finishTool(raw []byte, event nativeEvent) {
	if event.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	opening, known := a.tools[event.ToolCallID]
	delete(a.tools, event.ToolCallID)
	a.Mu.Unlock()
	if !known {
		return
	}
	a.persist(raw, agent.SpanInfo{SpanID: event.ToolCallID, SpanType: opening.name, Closing: true})
	a.sink.CloseSpan(event.ToolCallID)
	a.sink.ReportProgress(agent.CompleteOutputProgress(event.ToolCallID))
	a.finishChildTool(event, opening)
}

func (a *Agent) completeTurn(raw []byte, turnID, stopReason string) {
	if turnID == "" {
		return
	}
	a.Mu.Lock()
	if slices.Contains(a.finishedTurns, turnID) {
		a.Mu.Unlock()
		return
	}
	a.finishedTurns = append(a.finishedTurns, turnID)
	if len(a.finishedTurns) > 32 {
		a.finishedTurns = a.finishedTurns[len(a.finishedTurns)-32:]
	}
	active := a.turnID == turnID
	started, uses, usage := a.turnStarted, a.TurnToolUses, maps.Clone(a.usage)
	a.Mu.Unlock()
	if !active {
		return
	}
	completion := agent.MessageCompletionComplete
	if stopReason == "interrupted" {
		completion = agent.MessageCompletionInterrupted
	}
	if stopReason == "run_error" {
		completion = agent.MessageCompletionError
	}
	if err := a.generation.PersistAll(completion, func(content []byte) error {
		return a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content, Completion: completion}, agent.SpanInfo{})
	}); err != nil {
		slog.Warn("retain partial Command Code output", "error", err)
	}
	a.closeUnfinishedTools(completion)
	duration := max(int64(0), a.clock.Now().Sub(started).Milliseconds())
	metadata, err := json.Marshal(map[string]any{contracts.MessageMetadataFieldDurationMs: duration, contracts.SessionInfoKeyContextUsage: usage})
	if err != nil {
		slog.Warn("encode Command Code turn metadata", "error", err)
	}
	content := a.messageContent(raw)
	content.Completion, content.Metadata = completion, metadata
	content = agent.WithToolUseCount(content, uses)
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Warn("persist the Command Code turn end", "error", err)
	}
	a.Mu.Lock()
	if a.turnID == turnID {
		a.turnID = ""
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

func (a *Agent) closeUnfinishedTools(completion agent.MessageCompletion) {
	a.Mu.Lock()
	tools := a.tools
	a.tools = make(map[string]openTool)
	a.Mu.Unlock()
	for id, tool := range tools {
		content := a.messageContent(tool.opening)
		content.Completion = completion
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: id, SpanType: tool.name, Closing: true}); err != nil {
			slog.Warn("close an incomplete Command Code tool", "error", err)
		}
		a.sink.CloseSpan(id)
	}
}

func (a *Agent) finishStream() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	turnID := a.turnID
	compacting := a.compacting
	a.compacting = false
	if a.manualCompaction != nil {
		a.manualCompaction.outcome = "failed"
		a.manualCompaction.finish()
		a.manualCompaction = nil
	}
	a.Mu.Unlock()
	if turnID != "" {
		stopReason := "run_error"
		if a.ProcessExitCompletion() == agent.MessageCompletionInterrupted {
			stopReason = "interrupted"
		}
		// A process exit needs a boundary even when the native host emitted none.
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.CommandCodeMethodTurnCompleted, "params": map[string]any{"turnId": turnID, "stopReason": stopReason, "error": map[string]string{"message": "The Command Code process exited before the turn completed."}}})
		if err == nil {
			a.completeTurn(raw, turnID, stopReason)
		}
	}
	if a.sink != nil {
		if compacting && turnID == "" {
			a.PublishTurnActive()
		}
		a.closeChildren()
	}
}

func (a *Agent) recordUsage(usage *nativeUsage) {
	if usage == nil || usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 || usage.CacheWrite < 0 {
		return
	}
	reading := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite})
	a.Mu.Lock()
	model := agent.FindAvailableModel(a.models, a.model)
	if model != nil && model.ContextWindow > 0 {
		reading[contracts.ContextUsageFieldContextWindow] = model.ContextWindow
	}
	a.usage = reading
	a.Mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: reading})
}
