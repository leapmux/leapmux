package kimi

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const kimiCompactionBlockedTrigger = "waiting for the running turn to end"

type kimiTranscriptMarker struct {
	Kind     string `json:"kind"`
	MarkerID string `json:"markerId"`
	Marker   string `json:"marker"`
	At       string `json:"at"`
	Payload  struct {
		Blocked  bool            `json:"blocked"`
		Phase    string          `json:"phase"`
		Trigger  string          `json:"trigger"`
		Level    string          `json:"level"`
		Message  string          `json:"message"`
		Snapshot json.RawMessage `json:"snapshot"`
		Change   json.RawMessage `json:"change"`
		Event    struct {
			AgentID string `json:"agentId"`
			kimiEventErr
		} `json:"event"`
	} `json:"payload"`
	operation kimiTranscriptOperation
}

func decodeTranscriptMarker(op kimiTranscriptOperation) (kimiTranscriptMarker, bool) {
	var marker kimiTranscriptMarker
	if json.Unmarshal(op.Item, &marker) != nil || marker.Kind != contracts.KimiTranscriptNodeMarker || marker.MarkerID == "" {
		return marker, false
	}
	marker.operation = op
	return marker, true
}

func (a *Agent) handleTranscriptMarker(run *kimiRun, op kimiTranscriptOperation, source kimiFrame) {
	marker, valid := decodeTranscriptMarker(op)
	if !valid {
		return
	}
	a.applyTranscriptMarkerState(run, marker)
	a.applyTranscriptGoalMarker(run, marker, false)
	a.retainTranscriptMarker(run, marker, source)
}

func (a *Agent) applyTranscriptGoalMarker(run *kimiRun, marker kimiTranscriptMarker, snapshot bool) {
	if run.agentID != kimiMainAgentID || marker.Marker != contracts.KimiTranscriptMarkerGoal || len(marker.Payload.Snapshot) == 0 {
		return
	}
	var goal *kimiGoalSnapshot
	if err := json.Unmarshal(marker.Payload.Snapshot, &goal); err != nil {
		return
	}
	var change *kimiGoalChange
	if providerkit.RawPresent(marker.Payload.Change) {
		var decoded kimiGoalChange
		if json.Unmarshal(marker.Payload.Change, &decoded) == nil {
			change = &decoded
		}
	}
	a.applyGoal(goal, change, snapshot)
}

func (a *Agent) applyTranscriptMarkerState(run *kimiRun, marker kimiTranscriptMarker) {
	if marker.Marker != contracts.KimiTranscriptMarkerCompaction {
		return
	}
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	switch marker.Payload.Phase {
	case contracts.KimiCompactionPhaseStarted, contracts.KimiCompactionPhaseBlocked:
		run.compactionLive = true
		trigger := marker.Payload.Trigger
		if marker.Payload.Phase == contracts.KimiCompactionPhaseBlocked {
			trigger = kimiCompactionBlockedTrigger
		}
		providerkit.PublishCompaction(sink, trigger)
		if run.agentID == kimiMainAgentID && (marker.Payload.Phase == contracts.KimiCompactionPhaseStarted || marker.Payload.Trigger != "") {
			a.Mu.Lock()
			changed := a.manualCompactionActive != (marker.Payload.Trigger == "manual")
			a.manualCompactionActive = marker.Payload.Trigger == "manual"
			a.Mu.Unlock()
			if changed {
				a.PublishTurnActive()
			}
		}
	case contracts.KimiCompactionPhaseCompleted, contracts.KimiCompactionPhaseCancelled:
		a.endTranscriptCompaction(run)
	}
}

func (a *Agent) endTranscriptCompaction(run *kimiRun) {
	if sink := a.runSink(run); sink != nil {
		providerkit.EndCompaction(sink)
	}
	run.compactionLive = false
	if run.agentID == kimiMainAgentID {
		a.Mu.Lock()
		manual := a.manualCompactionActive
		a.manualCompactionActive = false
		a.Mu.Unlock()
		if manual {
			a.PublishTurnActive()
		}
	}
}

func (a *Agent) retainTranscriptMarker(run *kimiRun, marker kimiTranscriptMarker, source kimiFrame) {
	switch marker.Marker {
	case contracts.KimiTranscriptMarkerNotice, contracts.KimiTranscriptMarkerSkill, contracts.KimiTranscriptMarkerCronFired:
	case contracts.KimiTranscriptMarkerHook:
		if !marker.Payload.Blocked {
			return
		}
	case contracts.KimiTranscriptMarkerCompaction:
		if marker.Payload.Phase != contracts.KimiCompactionPhaseCompleted && marker.Payload.Phase != contracts.KimiCompactionPhaseCancelled {
			return
		}
	default:
		return
	}
	if a.IsDiscardingOutput() {
		return
	}
	if _, retained := run.transcript.storedMarkers[marker.MarkerID]; retained {
		return
	}
	if source.Type == contracts.KimiEventTranscriptReset {
		a.Mu.Lock()
		attachedAt := a.attachedAt
		a.Mu.Unlock()
		at, err := time.Parse(time.RFC3339Nano, marker.At)
		if !attachedAt.IsZero() && (err != nil || at.Before(attachedAt)) {
			return
		}
	}
	emptyNotice := marker.Marker == contracts.KimiTranscriptMarkerNotice && strings.TrimSpace(marker.Payload.Message) == "" && strings.TrimSpace(marker.Payload.Event.Code) == ""
	retainedByFailure := false
	if marker.Marker == contracts.KimiTranscriptMarkerNotice && marker.Payload.Level == contracts.KimiNoticeLevelError &&
		run.agentID == kimiMainAgentID && marker.Payload.Event.AgentID == run.agentID &&
		run.transcript.pendingFailure != nil && marker.Payload.Event.Message == *run.transcript.pendingFailure {
		record, err := kimiTranscriptRecord(source, run.agentID, marker.operation)
		if err != nil {
			slog.Error("encode Kimi failure notice", "agent_id", a.AgentID(), "error", err)
			return
		}
		run.transcript.pendingFailure = nil
		providerkit.ScheduleOrCancelServerErrorAutoRetry(a.sink, marker.Payload.Event.Retryable, record)
		// A nonblank failure already appears in its turn. An empty diagnostic has
		// no text to store, but its retry decision must still settle once.
		retainedByFailure = strings.TrimSpace(marker.Payload.Event.Message) != "" || emptyNotice
	}
	if emptyNotice && !retainedByFailure {
		return
	}
	if retainedByFailure || a.persistTranscriptNotification(run, marker.operation, source) {
		if run.transcript.storedMarkers == nil {
			run.transcript.storedMarkers = make(map[string]struct{})
		}
		run.transcript.storedMarkers[marker.MarkerID] = struct{}{}
	}
}

func (a *Agent) persistTranscriptNotification(run *kimiRun, op kimiTranscriptOperation, source kimiFrame) bool {
	sink := a.runSink(run)
	if sink == nil {
		return false
	}
	record, err := kimiTranscriptRecord(source, run.agentID, op)
	if err != nil {
		slog.Error("encode Kimi transcript notification", "agent_id", a.AgentID(), "error", err)
		return false
	}
	_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: record})
	if !agent.OutputRetained(err) {
		slog.Error("persist Kimi transcript notification", "agent_id", a.AgentID(), "error", err)
		return false
	}
	return true
}

type kimiTranscriptRetry struct {
	FailedAttempt int64    `json:"failedAttempt"`
	NextAttempt   int64    `json:"nextAttempt"`
	MaxAttempts   int64    `json:"maxAttempts"`
	DelayMS       *float64 `json:"delayMs"`
}

func (r *kimiTranscriptRetry) valid() bool {
	return r != nil && r.FailedAttempt >= 1 && r.NextAttempt > r.FailedAttempt && r.MaxAttempts >= r.NextAttempt && r.DelayMS != nil && *r.DelayMS >= 0
}

func (a *Agent) handleTranscriptRetry(run *kimiRun, op kimiTranscriptOperation, source kimiFrame, stepID string, retry *kimiTranscriptRetry) {
	step := run.transcript.turn(op.TurnID).step(stepID)
	if !retry.valid() || retry.NextAttempt <= step.retryAttempt {
		return
	}
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	for frameID, frame := range step.frames {
		if frame.kind == agent.AssembledMessageKindReasoning && !frame.finished {
			scope := kimiTranscriptScope(run.agentID, op.TurnID, stepID, frameID)
			run.generation.Discard(scope)
			sink.ReportProgress(agent.ResetModelScopeProgress(scope))
			frame.finished = true
			frame.text.Reset()
		}
	}
	a.finishTranscriptStep(run, op.TurnID, stepID, agent.MessageCompletionInterrupted)
	if !a.IsDiscardingOutput() && a.persistTranscriptNotification(run, op, source) {
		step.retryAttempt = retry.NextAttempt
		run.retry.NoteRetry()
	}
}
