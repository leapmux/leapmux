package kimi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type kimiTranscriptTool struct {
	call         *kimiTool
	opened       bool
	finished     bool
	linkedAgents map[string]struct{}
}

// closeRemovedTranscriptTools settles open calls absent from a complete replacement.
func (a *Agent) closeRemovedTranscriptTools(run *kimiRun) {
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	retained := make(map[*kimiTool]struct{})
	for _, turn := range run.transcript.turns {
		for _, step := range turn.steps {
			for _, tool := range step.tools {
				if !tool.finished {
					retained[tool.call] = struct{}{}
				}
			}
		}
	}
	removed := a.takeOpenToolCalls(run, func(tool *kimiTool) bool {
		_, present := retained[tool]
		return !present
	})
	a.closeToolCalls(sink, removed, agent.MessageCompletionInterrupted)
}

// closeTranscriptTurnTools settles only calls owned by this native turn.
func (a *Agent) closeTranscriptTurnTools(run *kimiRun, turn *kimiTranscriptTurn, completion agent.MessageCompletion) {
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	owned := make(map[*kimiTool]struct{})
	for _, step := range turn.steps {
		for _, tool := range step.tools {
			if !tool.finished {
				owned[tool.call] = struct{}{}
			}
		}
	}
	calls := a.takeOpenToolCalls(run, func(tool *kimiTool) bool { _, present := owned[tool]; return present })
	a.closeToolCalls(sink, calls, completion)
}

// restoreTranscriptToolProgress restores an incomplete output tail after a stream gap.
// A snapshot cannot reconstruct missing chunks or establish an exact total.
func (a *Agent) restoreTranscriptToolProgress(run *kimiRun, op kimiTranscriptOperation) {
	var frame kimiTranscriptToolFrame
	if json.Unmarshal(op.Frame, &frame) != nil || frame.State != contracts.KimiTranscriptToolRunning {
		return
	}
	tool := run.transcript.turn(op.TurnID).step(op.StepID).tools[frame.FrameID]
	sink := a.runSink(run)
	if tool == nil || tool.finished || sink == nil {
		return
	}
	a.Mu.Lock()
	tool.call.outputExact = false
	tool.call.tailLost = true
	if frame.Progress != nil && (frame.Progress.Kind == kimiProgressStdout || frame.Progress.Kind == kimiProgressStderr) && frame.Progress.Text != "" {
		tool.call.tail, _ = agent.ClipTailBytes(frame.Progress.Text, kimiLiveOutputLimit)
		tool.call.outputBytes = max(tool.call.outputBytes, int64(len(frame.Progress.Text)))
	}
	a.Mu.Unlock()
	a.reportToolProgress(tool.call, sink)
}

// kimiTranscriptRecord retains one native operation and its stream ownership.
// Snapshot entries use the same operation shape as live updates.
func kimiTranscriptRecord(source kimiFrame, agentID string, op kimiTranscriptOperation) ([]byte, error) {
	operation := op.raw
	if len(operation) == 0 {
		var value map[string]any
		switch op.Op {
		case contracts.KimiTranscriptOperationTurnUpsert:
			value = map[string]any{"op": op.Op, "turn": op.Turn}
		case contracts.KimiTranscriptOperationStepUpsert:
			value = map[string]any{"op": op.Op, "turnId": op.TurnID, "step": op.Step}
		case contracts.KimiTranscriptOperationFrameUpsert:
			value = map[string]any{"op": op.Op, "turnId": op.TurnID, "stepId": op.StepID, "frame": op.Frame}
		case contracts.KimiTranscriptOperationMarkerUpsert:
			value = map[string]any{"op": op.Op, "item": op.Item}
		}
		var err error
		operation, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	payload := map[string]any{"type": contracts.KimiEventTranscriptOps, "agent_id": agentID, "ops": []json.RawMessage{operation}}
	if source.transcriptSeq != nil {
		payload["seq"] = *source.transcriptSeq
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(kimiFrame{Type: contracts.KimiEventTranscriptOps, SessionID: source.SessionID, Timestamp: source.Timestamp, Payload: data})
}

func (a *Agent) handleTranscriptTool(run *kimiRun, op kimiTranscriptOperation, source kimiFrame) {
	var frame kimiTranscriptToolFrame
	if json.Unmarshal(op.Frame, &frame) != nil || frame.FrameID == "" || frame.ToolCallID == "" {
		return
	}
	if frame.State != contracts.KimiTranscriptToolRunning && frame.State != contracts.KimiTranscriptToolDone && frame.State != contracts.KimiTranscriptToolError {
		return
	}
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	if source.Type == contracts.KimiEventTranscriptOps && frame.State == contracts.KimiTranscriptToolRunning && frame.InputText != nil && !a.IsDiscardingOutput() {
		run.retry.Resolve(sink)
	}
	if frame.Name == "" || frame.State == contracts.KimiTranscriptToolRunning && len(frame.Input) == 0 {
		return
	}
	step := run.transcript.turn(op.TurnID).step(op.StepID)
	tool := step.tools[frame.FrameID]
	key := kimiTranscriptScope(run.agentID, op.TurnID, op.StepID, frame.FrameID)
	if tool == nil {
		a.Mu.Lock()
		sessionID := a.sessionID
		call := &kimiTool{spanID: sessionID + "/" + key, name: frame.Name, order: run.nextOrder, outputExact: source.Type == contracts.KimiEventTranscriptOps}
		run.nextOrder++
		a.Mu.Unlock()
		tool = &kimiTranscriptTool{call: call}
		step.tools[frame.FrameID] = tool
	}
	invocation := a.observeChildInvocation(run, op, frame, source, kimiInvocationPosition{turn: run.transcript.turn(op.TurnID).ordinal, step: step.ordinal}, true)
	if kimiToolSpawns(frame.Name) {
		spawn := kimiSpawnFromArgs(tool.call.spanID, frame.Name, frame.Input)
		if tool.linkedAgents == nil {
			tool.linkedAgents = make(map[string]struct{})
		}
		for index, ref := range frame.AgentRefs {
			if invocation == nil || invocation.targets[ref.AgentID] == nil || !a.invocationTargetsTrusted(run, invocation) {
				continue
			}
			if _, known := tool.linkedAgents[ref.AgentID]; known {
				continue
			}
			if child, linked := a.children.get(ref.AgentID); linked {
				if a.attachChildSpawnSpan(sink, child, kimiChildSpawnSpan(spawn, ref.AgentID)) {
					metadata := bgtask.Upsert{Kind: bgtask.KindSubagent}
					if err := a.updateChildRegistryMetadata(sink, ref.AgentID, metadata); agent.OutputRetained(err) {
						tool.linkedAgents[ref.AgentID] = struct{}{}
					} else {
						slog.Warn("update Kimi child spawn metadata", "agent_id", a.AgentID(), "child", ref.AgentID, "error", err)
					}
				}
				continue
			}
			swarmIndex := 0
			if ref.Role == "member" {
				swarmIndex = index + 1
			}
			a.linkSubagent(kimiChildAdoption{SubagentID: ref.AgentID, Description: spawn.label, SwarmIndex: swarmIndex}, run.agentID, spawn)
			if _, linked := a.children.get(ref.AgentID); linked {
				tool.linkedAgents[ref.AgentID] = struct{}{}
			}
		}
	}
	a.projectChildInvocation(run, invocation)
	if tool.finished || run.transcript.turn(op.TurnID).finished {
		return
	}
	record, err := kimiTranscriptRecord(source, run.agentID, op)
	if err != nil {
		slog.Error("encode Kimi transcript tool", "agent_id", a.AgentID(), "error", err)
		return
	}
	tool.call.lastFrame = record
	if !a.IsDiscardingOutput() {
		run.retry.Resolve(sink)
	}
	if frame.State == contracts.KimiTranscriptToolRunning {
		if !tool.opened {
			a.finishTranscriptStep(run, op.TurnID, op.StepID, agent.MessageCompletionComplete)
			err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: record}, tool.call.spanID, frame.Name, kimiToolSpawns(frame.Name))
			if !agent.OutputRetained(err) {
				slog.Error("persist Kimi transcript tool opening", "agent_id", a.AgentID(), "error", err)
				return
			}
			tool.opened = true
			a.Mu.Lock()
			run.tools[key] = tool.call
			a.Mu.Unlock()
			sink.ReportProgress(agent.ResetOutputProgress(tool.call.spanID))
		}
		// A snapshot repeats the last progress chunk; only live operations append it.
		if source.Type == contracts.KimiEventTranscriptOps && frame.Progress != nil && (frame.Progress.Kind == kimiProgressStdout || frame.Progress.Kind == kimiProgressStderr) {
			a.recordToolProgress(tool.call, sink, frame.Progress.Text)
		}
		return
	}
	span := agent.SpanInfo{SpanID: tool.call.spanID, SpanType: frame.Name, Closing: true}
	media, mediaErr := parseTranscriptMedia(source.SessionID, run.agentID, op, record)
	if mediaErr != nil {
		slog.Warn("recover Kimi tool images", "agent_id", a.AgentID(), "error", mediaErr)
	}
	if media != nil {
		retained, admissionErr := a.retainMediaResult(run, sink, media, record, span)
		if admissionErr != nil {
			slog.Error("reserve Kimi media result", "agent_id", a.AgentID(), "error", admissionErr)
			return
		}
		if retained {
			a.finishTranscriptTool(run, sink, tool, key)
			return
		}
	}
	content := agent.MessageContent{Original: record}
	if len(frame.Output) == 0 {
		content, _ = kimiRetainedTranscriptToolContent(record, tool.call.tail, tool.call.tailLost)
	}
	err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
	if !agent.OutputRetained(err) {
		slog.Error("persist Kimi transcript tool result", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.finishTranscriptTool(run, sink, tool, key)
}

func (a *Agent) finishTranscriptTool(run *kimiRun, sink agent.ProviderServices, tool *kimiTranscriptTool, key string) {
	tool.finished = true
	a.Mu.Lock()
	tool.call.lastFrame = nil
	tool.call.tail = ""
	delete(run.tools, key)
	if run.agentID == kimiMainAgentID {
		a.TurnToolUses++
	}
	a.Mu.Unlock()
	sink.ReportProgress(agent.CompleteOutputProgress(tool.call.spanID))
	sink.CloseSpan(tool.call.spanID)
}
