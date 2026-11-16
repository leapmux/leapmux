package kimi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// kimiTranscript follows native transcript positions independently for each agent.
// dispatchMu protects it, including calls made through HandleOutput.
type kimiTranscript struct {
	seq            int64
	hasSeq         bool
	turns          map[string]*kimiTranscriptTurn
	activeTurn     string
	storedMarkers  map[string]struct{}
	pendingFailure *string
}

type kimiTranscriptTurn struct {
	triggerPromptID string
	consumedPrompts map[string]map[string]struct{}
	ordinal         *int64
	steps           map[string]*kimiTranscriptStep
	started         bool
	finished        bool
}

type kimiTranscriptStep struct {
	ordinal      *int64
	retryAttempt int64
	frames       map[string]*kimiTranscriptText
	tools        map[string]*kimiTranscriptTool
	order        []string
}

type kimiTranscriptText struct {
	kind     agent.AssembledMessageKind
	text     strings.Builder
	units    int64
	finished bool
	// persistedBytes identifies the prefix already owned by immutable output writes.
	persistedBytes int
}

type kimiTranscriptOperation struct {
	raw         json.RawMessage
	Op          string                  `json:"op"`
	TurnID      string                  `json:"turnId"`
	StepID      string                  `json:"stepId"`
	Turn        json.RawMessage         `json:"turn"`
	Step        json.RawMessage         `json:"step"`
	Frame       json.RawMessage         `json:"frame"`
	Prompt      json.RawMessage         `json:"prompt"`
	Item        json.RawMessage         `json:"item"`
	Interaction json.RawMessage         `json:"interaction"`
	AgentID     string                  `json:"agentId"`
	Snapshot    *kimiTranscriptSnapshot `json:"snapshot"`
	Target      struct {
		Type    string `json:"type"`
		TurnID  string `json:"turnId"`
		StepID  string `json:"stepId"`
		FrameID string `json:"frameId"`
	} `json:"target"`
	Offset *int64 `json:"offset"`
	Text   string `json:"text"`
}

type kimiTranscriptSnapshot struct {
	Items        []json.RawMessage `json:"items"`
	Interactions json.RawMessage   `json:"interactions"`
	Prompts      []json.RawMessage `json:"prompts"`
	HasMoreOlder bool              `json:"hasMoreOlder"`
}

type kimiTranscriptReset struct {
	AgentID      string                  `json:"agent_id"`
	Seq          *int64                  `json:"seq"`
	HasMoreOlder bool                    `json:"has_more_older"`
	Snapshot     *kimiTranscriptSnapshot `json:"snapshot"`
}

type kimiTranscriptOrigin struct {
	Kind    string      `json:"kind"`
	Payload *kimiOrigin `json:"payload"`
}

func (o kimiTranscriptOrigin) originalKind() string {
	if o.Payload != nil && o.Payload.Kind != "" {
		return o.Payload.Kind
	}
	return o.Kind
}

func (op *kimiTranscriptOperation) UnmarshalJSON(data []byte) error {
	type operation kimiTranscriptOperation
	var decoded operation
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*op = kimiTranscriptOperation(decoded)
	op.raw = append(json.RawMessage(nil), data...)
	return nil
}

func (t *kimiTranscript) turn(id string) *kimiTranscriptTurn {
	if t.turns == nil {
		t.turns = make(map[string]*kimiTranscriptTurn)
	}
	turn := t.turns[id]
	if turn == nil {
		turn = &kimiTranscriptTurn{steps: make(map[string]*kimiTranscriptStep)}
		t.turns[id] = turn
	}
	return turn
}

func (t *kimiTranscriptTurn) step(id string) *kimiTranscriptStep {
	step := t.steps[id]
	if step == nil {
		step = &kimiTranscriptStep{frames: make(map[string]*kimiTranscriptText), tools: make(map[string]*kimiTranscriptTool)}
		t.steps[id] = step
	}
	return step
}

func kimiTranscriptScope(agentID, turnID, stepID, frameID string) string {
	return fmt.Sprintf("kimi/%q/%q/%q/%q", agentID, turnID, stepID, frameID)
}

func (a *Agent) handleTranscriptOperations(frame kimiFrame) {
	if !a.nativeTranscriptSession(frame.SessionID) {
		return
	}
	var payload struct {
		AgentID string                    `json:"agent_id"`
		Seq     *int64                    `json:"seq"`
		Ops     []kimiTranscriptOperation `json:"ops"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil || payload.AgentID == "" || payload.Seq == nil || *payload.Seq < 0 {
		slog.Warn("invalid Kimi transcript operations", "agent_id", a.AgentID())
		return
	}
	run := a.run(payload.AgentID)
	frame.transcriptSeq = payload.Seq
	if !run.pendingNativeRecovery && run.transcript.hasSeq && *payload.Seq <= run.transcript.seq {
		return
	}
	// Refuse a malformed replacement before its control list can withdraw requests.
	for _, operation := range payload.Ops {
		if operation.Op == contracts.KimiTranscriptOperationReset && operation.AgentID == payload.AgentID && operation.Snapshot != nil && operation.Snapshot.Items != nil {
			if err := a.validateTranscriptReplacement(run, operation.Snapshot); err != nil {
				return
			}
		}
	}
	// Controls are session-wide and can precede adoption of their agent's transcript.
	for _, operation := range payload.Ops {
		if operation.Op == contracts.KimiTranscriptOperationReset && operation.AgentID == payload.AgentID && operation.Snapshot != nil && operation.Snapshot.Items != nil {
			a.reconcileTranscriptControls(frame.SessionID, payload.AgentID, operation.Snapshot.Interactions)
		}
		if operation.Op == contracts.KimiTranscriptOperationInteractionUpsert {
			if interaction, valid := decodeTranscriptInteraction(operation.Interaction); valid && interaction.State != kimiInteractionPending {
				a.resolveControl(interaction.identity(frame.SessionID, payload.AgentID))
			}
		}
	}
	if a.holdNativeTranscriptFrame(payload.AgentID, frame) {
		return
	}
	for _, operation := range payload.Ops {
		a.applyTranscriptOperation(run, operation, frame)
		if run.pendingNativeRecovery {
			a.queueNativeTranscriptFrame(run, frame)
			return
		}
	}
	run.transcript.seq, run.transcript.hasSeq = *payload.Seq, true
	a.replayNativeTasksOwnedBy(run.agentID)
}

// handleTranscriptReset replaces unfinished output with the native snapshot.
// Finished frames keep their identity so a reconnect cannot publish them twice.
func (a *Agent) handleTranscriptReset(frame kimiFrame) {
	var payload kimiTranscriptReset
	if json.Unmarshal(frame.Payload, &payload) != nil {
		slog.Warn("invalid Kimi transcript reset", "agent_id", a.AgentID())
		return
	}
	a.applyTranscriptReset(frame, payload)
}

func (a *Agent) applyTranscriptReset(frame kimiFrame, payload kimiTranscriptReset) {
	if !a.nativeTranscriptSession(frame.SessionID) || payload.AgentID == "" || payload.Snapshot == nil || payload.Snapshot.Items == nil || (payload.Seq != nil && *payload.Seq < 0) {
		slog.Warn("invalid Kimi transcript reset", "agent_id", a.AgentID())
		return
	}
	if err := a.validateTranscriptReplacement(a.run(payload.AgentID), payload.Snapshot); err != nil {
		slog.Warn("refuse malformed Kimi transcript snapshot", "agent_id", a.AgentID(), "error", err)
		return
	}
	frame.transcriptSeq = payload.Seq
	a.reconcileTranscriptControls(frame.SessionID, payload.AgentID, payload.Snapshot.Interactions)
	if payload.HasMoreOlder || payload.Snapshot.HasMoreOlder {
		run := a.run(payload.AgentID)
		a.requireNativeTranscriptRecovery(run)
		return
	}
	run := a.run(payload.AgentID)
	if run.pendingNativeRecovery {
		a.takePendingNativeFrames(run)
		run.pendingNativeRecovery = false
		run.nativeRecoveryRevision++
	}
	if run.agentID != kimiMainAgentID {
		if _, linked := a.children.get(run.agentID); !linked {
			a.takePendingNativeFrames(run)
			a.queueNativeTranscriptFrame(run, frame)
			return
		}
	}
	for _, raw := range payload.Snapshot.Prompts {
		a.applyNativePromptRecord(run, frame.SessionID, raw)
	}
	// Snapshot frames repeat retained progress; they do not deliver new chunks.
	frame.Type = contracts.KimiEventTranscriptReset
	previous := run.transcript
	progressGap := !previous.hasSeq || payload.Seq == nil || *payload.Seq != previous.seq
	for turnID, turn := range previous.turns {
		for stepID, step := range turn.steps {
			for frameID, text := range step.frames {
				if !text.finished {
					scope := kimiTranscriptScope(run.agentID, turnID, stepID, frameID)
					run.generation.Discard(scope)
					if sink := a.runSink(run); sink != nil {
						sink.ReportProgress(agent.ResetModelScopeProgress(scope))
					}
				}
			}
		}
	}
	runningTurnID := ""
	var runningOrigin kimiTranscriptOrigin
	for _, raw := range payload.Snapshot.Items {
		var header struct {
			Kind   string               `json:"kind"`
			TurnID string               `json:"turnId"`
			State  string               `json:"state"`
			Origin kimiTranscriptOrigin `json:"origin"`
		}
		if json.Unmarshal(raw, &header) == nil && header.Kind == contracts.KimiTranscriptNodeTurn && header.TurnID != "" && header.State == contracts.KimiTranscriptTurnRunning {
			runningTurnID, runningOrigin = header.TurnID, header.Origin
		}
	}
	activeTurnID := runningTurnID
	if activeTurnID == "" {
		activeTurnID = previous.activeTurn
	}
	run.transcript = kimiTranscript{
		turns: make(map[string]*kimiTranscriptTurn), activeTurn: activeTurnID,
		storedMarkers: previous.storedMarkers, pendingFailure: previous.pendingFailure,
	}
	a.preScanChildInvocations(run, frame, payload.Snapshot, previous)
	if runningTurnID != "" {
		run.transcript.pendingFailure = nil
	}
	a.Mu.Lock()
	attachedAt := a.attachedAt
	run.turnActive = runningTurnID != ""
	if run.turnActive {
		run.userTurn = runningOrigin.Kind == contracts.KimiOriginUser
	}
	if run.agentID == kimiMainAgentID {
		a.turnActive = run.turnActive
		a.turnSteerable = run.turnActive && kimiOriginTakesSteer(runningOrigin.originalKind())
	}
	a.Mu.Unlock()
	if run.agentID != kimiMainAgentID && runningTurnID != "" {
		if _, linked := a.children.get(run.agentID); linked && a.activateChildLifetime(run) {
			// Captured records must belong to the restored running scope.
			a.publishChildTurn(run, true)
		}
	}
	if payload.Seq != nil {
		run.transcript.seq, run.transcript.hasSeq = *payload.Seq, true
	}
	var lastCompaction *kimiTranscriptMarker
	var lastGoal *kimiTranscriptMarker
	for _, raw := range payload.Snapshot.Items {
		var turn struct {
			Origin  kimiTranscriptOrigin `json:"origin"`
			Kind    string               `json:"kind"`
			TurnID  string               `json:"turnId"`
			State   string               `json:"state"`
			EndedAt string               `json:"endedAt"`
			Steps   []json.RawMessage    `json:"steps"`
		}
		if json.Unmarshal(raw, &turn) != nil {
			continue
		}
		if turn.Kind == contracts.KimiTranscriptNodeMarker {
			marker, valid := decodeTranscriptMarker(kimiTranscriptOperation{Op: contracts.KimiTranscriptOperationMarkerUpsert, Item: raw})
			if !valid {
				continue
			}
			switch marker.Marker {
			case contracts.KimiTranscriptMarkerGoal:
				lastGoal = &marker
			case contracts.KimiTranscriptMarkerCompaction:
				if marker.Payload.Phase == contracts.KimiCompactionPhaseBlocked && marker.Payload.Trigger == "" && lastCompaction != nil {
					marker.Payload.Trigger = lastCompaction.Payload.Trigger
				}
				lastCompaction = &marker
			default:
				a.applyTranscriptMarkerState(run, marker)
			}
			a.retainTranscriptMarker(run, marker, frame)
			continue
		}
		if turn.Kind != contracts.KimiTranscriptNodeTurn || turn.TurnID == "" {
			continue
		}
		prior := previous.turns[turn.TurnID]
		if prior != nil && prior.finished {
			run.transcript.turns[turn.TurnID] = prior
			continue
		}
		if prior == nil && turn.State != contracts.KimiTranscriptTurnRunning && turn.State != contracts.KimiTranscriptTurnQueued {
			endedAt, err := time.Parse(time.RFC3339Nano, turn.EndedAt)
			if !attachedAt.IsZero() && (err != nil || endedAt.Before(attachedAt)) {
				run.transcript.turn(turn.TurnID).finished = true
				continue
			}
		}
		if run.agentID != kimiMainAgentID && prior == nil && runningTurnID == "" && turn.State != contracts.KimiTranscriptTurnRunning && turn.State != contracts.KimiTranscriptTurnQueued {
			if !a.restoreCompletedChildTurn(run, turn.TurnID, turn.Origin) {
				continue
			}
		}
		if prior != nil {
			run.transcript.turn(turn.TurnID).started = prior.started
		}
		if turn.State == contracts.KimiTranscriptTurnRunning {
			a.applyTranscriptOperation(run, kimiTranscriptOperation{Op: contracts.KimiTranscriptOperationTurnUpsert, Turn: raw}, frame)
		}
		for _, stepRaw := range turn.Steps {
			var step struct {
				StepID string            `json:"stepId"`
				Frames []json.RawMessage `json:"frames"`
			}
			if json.Unmarshal(stepRaw, &step) != nil || step.StepID == "" {
				continue
			}
			currentStep := run.transcript.turn(turn.TurnID).step(step.StepID)
			if prior != nil && prior.steps[step.StepID] != nil {
				currentStep.retryAttempt = prior.steps[step.StepID].retryAttempt
			}
			for _, frameRaw := range step.Frames {
				var identity struct {
					FrameID string `json:"frameId"`
					Kind    string `json:"kind"`
				}
				if json.Unmarshal(frameRaw, &identity) != nil || identity.FrameID == "" {
					continue
				}
				if prior != nil && prior.steps[step.StepID] != nil {
					if tool := prior.steps[step.StepID].tools[identity.FrameID]; identity.Kind == contracts.KimiTranscriptFrameTool && tool != nil {
						currentStep.tools[identity.FrameID] = tool
					}
					if text := prior.steps[step.StepID].frames[identity.FrameID]; text != nil && (text.finished || text.persistedBytes > 0) {
						currentStep.frames[identity.FrameID] = text
						currentStep.order = append(currentStep.order, identity.FrameID)
					}
				}
				operation := kimiTranscriptOperation{Op: contracts.KimiTranscriptOperationFrameUpsert, TurnID: turn.TurnID, StepID: step.StepID, Frame: frameRaw}
				a.applyTranscriptOperation(run, operation, frame)
				if progressGap && identity.Kind == contracts.KimiTranscriptFrameTool {
					a.restoreTranscriptToolProgress(run, operation)
				}
			}
			a.applyTranscriptOperation(run, kimiTranscriptOperation{Op: contracts.KimiTranscriptOperationStepUpsert, TurnID: turn.TurnID, Step: stepRaw}, frame)
		}
		if turn.State != contracts.KimiTranscriptTurnRunning {
			a.applyTranscriptOperation(run, kimiTranscriptOperation{Op: contracts.KimiTranscriptOperationTurnUpsert, Turn: raw}, frame)
		}
	}
	for _, invocation := range run.childInvocations {
		if !invocation.present {
			invocation.finished = true
			invocation.finishUnmatchedTargets()
		}
		a.projectChildInvocation(run, invocation)
	}
	if lastGoal != nil {
		a.applyTranscriptGoalMarker(run, *lastGoal, true)
	}
	if lastCompaction != nil {
		a.applyTranscriptMarkerState(run, *lastCompaction)
	} else if run.compactionLive {
		a.endTranscriptCompaction(run)
	}
	a.replayNativeTasksOwnedBy(run.agentID)
	a.closeRemovedTranscriptTools(run)
	if run.agentID == kimiMainAgentID {
		a.PublishTurnActive()
	} else if child, linked := a.children.get(run.agentID); linked {
		if !run.turnActive {
			run.transcript.activeTurn = ""
			a.Mu.Lock()
			run.userTurn = false
			a.Mu.Unlock()
			a.publishChildTurn(run, false)
			// The complete snapshot owns current activity even when the content
			// cutoff excludes its old completed turns or it contains no turns.
			settled := child.ended
			if !settled {
				settled = agent.OutputRetained(a.closeSubagentRegistryRow(run.agentID, child, bgtask.StatusEndedWithUnknownOutcome))
			}
			if settled {
				a.queueChildCleanup(run)
			}
		}
	}
}

func (a *Agent) applyTranscriptOperation(run *kimiRun, op kimiTranscriptOperation, source kimiFrame) {
	switch op.Op {
	case contracts.KimiTranscriptOperationReset:
		if op.AgentID == run.agentID {
			a.applyTranscriptReset(source, kimiTranscriptReset{AgentID: run.agentID, Seq: source.transcriptSeq, Snapshot: op.Snapshot})
		}
	case contracts.KimiTranscriptOperationPromptUpsert:
		a.applyNativePromptRecord(run, source.SessionID, op.Prompt)
	case contracts.KimiTranscriptOperationMarkerUpsert:
		a.handleTranscriptMarker(run, op, source)
	case contracts.KimiTranscriptOperationTurnUpsert:
		var header struct {
			TriggerPromptID string               `json:"triggerPromptId"`
			TurnID          string               `json:"turnId"`
			Ordinal         *int64               `json:"ordinal"`
			State           string               `json:"state"`
			Origin          kimiTranscriptOrigin `json:"origin"`
			Prompt          string               `json:"prompt"`
			Error           *string              `json:"error"`
		}
		if json.Unmarshal(op.Turn, &header) != nil || header.TurnID == "" {
			return
		}
		turn := run.transcript.turn(header.TurnID)
		if !validKimiTranscriptTurnState(header.State) {
			return
		}
		if !a.observePromptTrigger(run, turn, source.SessionID, header.TriggerPromptID) {
			return
		}
		if header.Ordinal != nil {
			turn.ordinal = header.Ordinal
		}
		if turn.finished {
			return
		}
		if header.State == contracts.KimiTranscriptTurnRunning {
			run.transcript.pendingFailure = nil
			run.transcript.activeTurn = header.TurnID
			if run.agentID == kimiMainAgentID {
				a.Mu.Lock()
				if !turn.started {
					a.TurnToolUses = 0
				}
				a.turnActive, a.turnSteerable = true, kimiOriginTakesSteer(header.Origin.originalKind())
				run.turnActive = true
				a.Mu.Unlock()
				a.PublishTurnActive()
				if !turn.started && (kimiOriginIsNotable(header.Origin.originalKind()) || header.Origin.Kind == "task" && strings.TrimSpace(header.Prompt) != "") {
					a.persistTranscriptNotification(run, op, source)
				}
			} else {
				if !turn.started {
					a.startChildTranscriptTurn(run, header.Origin.Kind, header.Prompt)
				} else if _, linked := a.children.get(run.agentID); linked {
					a.Mu.Lock()
					wasActive := run.turnActive
					run.turnActive = true
					run.userTurn = header.Origin.Kind == contracts.KimiOriginUser
					a.Mu.Unlock()
					if !wasActive {
						a.publishChildTurn(run, true)
					}
				}
			}
			turn.started = true
			return
		}
		if header.State != contracts.KimiTranscriptTurnCompleted && header.State != contracts.KimiTranscriptTurnFailed && header.State != contracts.KimiTranscriptTurnCancelled {
			return
		}
		completion := kimiTurnCompletion(header.State)
		endingCurrentTurn := run.transcript.activeTurn == "" || run.transcript.activeTurn == header.TurnID
		stepIDs := slices.Collect(maps.Keys(turn.steps))
		slices.SortFunc(stepIDs, func(left, right string) int {
			if order := cmp.Compare(kimiOrdinalValue(turn.steps[left].ordinal), kimiOrdinalValue(turn.steps[right].ordinal)); order != 0 {
				return order
			}
			return cmp.Compare(left, right)
		})
		toolUses := 0
		for _, stepID := range stepIDs {
			a.finishTranscriptStep(run, header.TurnID, stepID, completion)
			for _, tool := range turn.steps[stepID].tools {
				if tool.finished {
					toolUses++
				}
			}
		}
		a.closeTranscriptTurnTools(run, turn, completion)
		a.finishChildInvocationTurn(run, header.TurnID)
		if run.agentID == kimiMainAgentID {
			record, err := kimiTranscriptRecord(source, run.agentID, op)
			if err != nil {
				slog.Error("encode Kimi transcript turn end", "agent_id", a.AgentID(), "error", err)
				return
			}
			content := agent.WithToolUseCount(agent.MessageContent{Original: record}, toolUses)
			a.Mu.Lock()
			usage := maps.Clone(a.contextUsage)
			a.Mu.Unlock()
			content = kimiWithContextUsage(content, usage)
			if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
				slog.Error("persist Kimi transcript turn end", "agent_id", a.AgentID(), "error", err)
			}
			if run.transcript.activeTurn == header.TurnID {
				a.sink.ResetSpans()
				a.Mu.Lock()
				a.turnActive, a.turnSteerable, run.turnActive = false, false, false
				a.Mu.Unlock()
				run.transcript.activeTurn = ""
			}
			if endingCurrentTurn {
				providerkit.ScheduleOrCancelServerErrorAutoRetry(a.sink, false, nil)
				run.transcript.pendingFailure = nil
				if header.State == contracts.KimiTranscriptTurnFailed {
					run.transcript.pendingFailure = header.Error
				}
				a.PublishTurnActive()
				a.sink.ReportProgress(agent.ResetModelProgress())
				a.requestUsageRefresh()
			}
		} else {
			a.finishNativeChildTurn(run, header.TurnID, header.State, source)
		}
		if endingCurrentTurn {
			run.retry.Reset()
		}
		turn.finished = true
		for _, step := range turn.steps {
			clear(step.frames)
			step.order = nil
		}
	case contracts.KimiTranscriptOperationStepUpsert:
		var header struct {
			StepID  string               `json:"stepId"`
			State   string               `json:"state"`
			Ordinal *int64               `json:"ordinal"`
			Retry   *kimiTranscriptRetry `json:"retry"`
		}
		if op.TurnID == "" || json.Unmarshal(op.Step, &header) != nil || header.StepID == "" {
			return
		}
		turn := run.transcript.turn(op.TurnID)
		if turn.finished {
			return
		}
		step := turn.step(header.StepID)
		if header.Ordinal != nil {
			step.ordinal = header.Ordinal
		}
		if header.State == contracts.KimiTranscriptStepRunning && header.Retry != nil {
			a.handleTranscriptRetry(run, op, source, header.StepID, header.Retry)
		}
		if header.State == contracts.KimiTranscriptStepCompleted || header.State == contracts.KimiTranscriptStepFailed || header.State == contracts.KimiTranscriptStepInterrupted {
			completion := agent.MessageCompletionComplete
			switch header.State {
			case "failed":
				completion = agent.MessageCompletionError
			case "interrupted":
				completion = agent.MessageCompletionInterrupted
			}
			a.finishTranscriptStep(run, op.TurnID, header.StepID, completion)
			if run.agentID == kimiMainAgentID {
				a.recordStepUsage(kimiEvent{Raw: op.Step})
			}
		}
	case contracts.KimiTranscriptOperationFrameUpsert:
		var frame struct {
			Kind    string `json:"kind"`
			FrameID string `json:"frameId"`
			Role    string `json:"role"`
			Text    string `json:"text"`
			TaskID  string `json:"taskId"`
		}
		if op.TurnID == "" || op.StepID == "" || json.Unmarshal(op.Frame, &frame) != nil || frame.FrameID == "" {
			return
		}
		a.observeConsumedPrompts(run, source.SessionID, op.TurnID, op.StepID, op.Frame)
		if run.transcript.turn(op.TurnID).finished && frame.Kind != contracts.KimiTranscriptFrameTool {
			return
		}
		if frame.Kind == contracts.KimiTranscriptFrameTool {
			a.handleTranscriptTool(run, op, source)
			return
		}
		if frame.Kind == contracts.KimiTranscriptFrameText && frame.Role == contracts.KimiTranscriptRoleUser && frame.TaskID != "" {
			step := run.transcript.turn(op.TurnID).step(op.StepID)
			if existing := step.frames[frame.FrameID]; existing != nil && existing.finished {
				return
			}
			if strings.TrimSpace(frame.Text) != "" && a.persistTranscriptNotification(run, op, source) {
				step.frames[frame.FrameID] = &kimiTranscriptText{kind: agent.AssembledMessageKindText, finished: true}
			}
			return
		}
		kind := agent.AssembledMessageKindText
		if frame.Kind == contracts.KimiTranscriptFrameThinking {
			kind = agent.AssembledMessageKindReasoning
		} else if frame.Kind != contracts.KimiTranscriptFrameText || frame.Role != contracts.KimiTranscriptRoleAssistant {
			return
		}
		step := run.transcript.turn(op.TurnID).step(op.StepID)
		if kind == agent.AssembledMessageKindText {
			for _, frameID := range step.order {
				previous := step.frames[frameID]
				if previous.kind == agent.AssembledMessageKindReasoning {
					a.persistTranscriptTextScope(run, kimiTranscriptScope(run.agentID, op.TurnID, op.StepID, frameID), previous, agent.MessageCompletionComplete)
				}
			}
		}
		text := step.frames[frame.FrameID]
		if text == nil {
			text = &kimiTranscriptText{kind: kind}
			step.frames[frame.FrameID] = text
			step.order = append(step.order, frame.FrameID)
		}
		if !text.finished {
			a.replaceTranscriptText(run, op.TurnID, op.StepID, frame.FrameID, text, frame.Text)
		}
	case contracts.KimiTranscriptOperationAppend:
		if op.Target.Type != contracts.KimiTranscriptNodeFrame || op.Offset == nil {
			return
		}
		turn := run.transcript.turns[op.Target.TurnID]
		if turn == nil || turn.steps[op.Target.StepID] == nil {
			return
		}
		text := turn.steps[op.Target.StepID].frames[op.Target.FrameID]
		if text == nil || text.finished {
			return
		}
		if *op.Offset == text.units {
			a.appendTranscriptText(run, op.Target.TurnID, op.Target.StepID, op.Target.FrameID, text, op.Text)
			return
		}
		merged, ok := kimiAppendTranscriptText(text.text.String(), *op.Offset, op.Text)
		if !ok {
			slog.Warn("Kimi transcript text has a gap", "agent_id", a.AgentID(), "frame_id", op.Target.FrameID)
			return
		}
		a.replaceTranscriptText(run, op.Target.TurnID, op.Target.StepID, op.Target.FrameID, text, merged)
	}
}

func kimiOrdinalValue(ordinal *int64) int64 {
	if ordinal == nil {
		return 0
	}
	return *ordinal
}

func (a *Agent) replaceTranscriptText(run *kimiRun, turnID, stepID, frameID string, frame *kimiTranscriptText, text string) {
	previous := frame.text.String()
	if previous == text {
		a.syncTranscriptTextBuffer(run, turnID, stepID, frameID, frame)
		return
	}
	if strings.HasPrefix(text, previous) {
		a.syncTranscriptTextBuffer(run, turnID, stepID, frameID, frame)
		a.appendTranscriptText(run, turnID, stepID, frameID, frame, text[len(previous):])
		return
	}
	if frame.persistedBytes > len(text) || !strings.HasPrefix(text, previous[:frame.persistedBytes]) {
		frame.persistedBytes = 0
	}
	frame.text.Reset()
	frame.text.WriteString(text)
	frame.units = kimiTranscriptTextUnits(text)
	a.syncTranscriptTextBuffer(run, turnID, stepID, frameID, frame)
}

// syncTranscriptTextBuffer rebuilds only the bytes not retained by an output write.
func (a *Agent) syncTranscriptTextBuffer(run *kimiRun, turnID, stepID, frameID string, frame *kimiTranscriptText) {
	scope := kimiTranscriptScope(run.agentID, turnID, stepID, frameID)
	unpersisted := frame.text.String()[frame.persistedBytes:]
	kind, buffered, present := run.generation.ScopeSnapshot(scope)
	if (!present && unpersisted == "") || (present && kind == frame.kind && buffered == unpersisted) {
		return
	}
	run.generation.Discard(scope)
	if unpersisted != "" {
		run.generation.Append(scope, frame.kind, unpersisted, providerkit.JoinVerbatim)
	}
	if sink := a.runSink(run); sink != nil && !a.IsDiscardingOutput() {
		sink.ReportProgress(agent.ResetModelScopeProgress(scope))
		if unpersisted != "" {
			sink.ReportProgress(agent.ModelTextProgress(scope, unpersisted))
		}
	}
}

func (a *Agent) recordFlushedTranscriptScopes(run *kimiRun, scopes []string) {
	retained := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		retained[scope] = struct{}{}
	}
	for _, scope := range run.generation.ScopeIDs() {
		delete(retained, scope)
	}
	for turnID, turn := range run.transcript.turns {
		for stepID, step := range turn.steps {
			for frameID, frame := range step.frames {
				if frame.finished {
					continue
				}
				if _, captured := retained[kimiTranscriptScope(run.agentID, turnID, stepID, frameID)]; captured {
					frame.persistedBytes = frame.text.Len()
				}
			}
		}
	}
}

func (a *Agent) appendTranscriptText(run *kimiRun, turnID, stepID, frameID string, frame *kimiTranscriptText, delta string) {
	if delta == "" {
		return
	}
	scope := kimiTranscriptScope(run.agentID, turnID, stepID, frameID)
	run.generation.Append(scope, frame.kind, delta, providerkit.JoinVerbatim)
	frame.text.WriteString(delta)
	frame.units += kimiTranscriptTextUnits(delta)
	if sink := a.runSink(run); sink != nil && !a.IsDiscardingOutput() {
		run.retry.Resolve(sink)
		sink.ReportProgress(agent.ModelTextProgress(scope, delta))
	}
}

func kimiTranscriptTextUnits(text string) int64 {
	var units int64
	for _, character := range text {
		units++
		if character > 0xffff {
			units++
		}
	}
	return units
}

func (a *Agent) finishTranscriptStep(run *kimiRun, turnID, stepID string, completion agent.MessageCompletion) {
	step := run.transcript.turn(turnID).step(stepID)
	for _, frameID := range step.order {
		a.persistTranscriptTextScope(run, kimiTranscriptScope(run.agentID, turnID, stepID, frameID), step.frames[frameID], completion)
	}
}

func (a *Agent) persistTranscriptTextScope(run *kimiRun, scope string, frame *kimiTranscriptText, completion agent.MessageCompletion) {
	sink := a.runSink(run)
	if sink == nil || frame.finished {
		return
	}
	err := providerkit.PersistGenerationScopes(&run.generation, sink, []string{scope}, completion, a.IsDiscardingOutput())
	if err != nil {
		slog.Error("persist Kimi transcript text", "agent_id", a.AgentID(), "error", err)
	}
	frame.finished = agent.OutputRetained(err)
	if frame.finished {
		frame.text.Reset()
	}
}

// kimiAppendTranscriptText uses JavaScript UTF-16 offsets and accepts repeated chunks.
// A gap leaves the authoritative local text unchanged.
func kimiAppendTranscriptText(local string, offset int64, chunk string) (string, bool) {
	previous, next := utf16.Encode([]rune(local)), utf16.Encode([]rune(chunk))
	if offset < 0 || offset > int64(len(previous)) {
		return local, false
	}
	start := int(offset)
	if len(next) <= len(previous)-start && slices.Equal(previous[start:start+len(next)], next) {
		return local, true
	}
	overlap := len(previous) - start
	if overlap > len(next) || !slices.Equal(previous[start:], next[:overlap]) {
		return local, false
	}
	return string(utf16.Decode(append(previous[:start], next...))), true
}
