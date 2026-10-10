package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// goalAdmission retains existing registration and session facts for one operation.
// Ordinary later turns do not change these facts.
type goalAdmission struct {
	h         *OutputHandler
	agentID   string
	sink      *agentOutputSink
	publisher *agentOutputSink
	session   *nativeSessionFact
}

// goalOperation retains the original transcript context before a provider callback can replace it.
type goalOperation struct {
	admission *goalAdmission
	content   agent.MessageContent
}

func (sink *agentOutputSink) captureGoalOperation() goalOperation {
	content := sink.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	owner := content.Publication.Owner().(*transcriptOwner)
	return goalOperation{
		admission: &goalAdmission{
			h:         sink.h,
			agentID:   sink.agentID,
			sink:      sink,
			publisher: sink.turnPublisherSink(),
			session:   owner.session,
		},
		content: content,
	}
}

// captureGoalProjection keeps an available exact source for a direct capability projection.
// An absent source stays absent. A later registration invalidates that operation.
func (h *OutputHandler) captureGoalProjection(agentID string) goalOperation {
	if sink := h.sinkForAgent(agentID); sink != nil {
		return sink.captureGoalOperation()
	}
	return goalOperation{admission: &goalAdmission{h: h, agentID: agentID}}
}

// errGoalWriterRefused names one policy for a goal writer that cannot exist:
// the refusal and a failed write share it, so a retrying caller treats both
// the same instead of reading a nil writer and a nil error as success.
var errGoalWriterRefused = errors.New("the captured goal write has no current authority")

// errGoalWriterExpired names the refusal a captured goal writer returns once
// its publication's session authority is gone: a context replacement replaced
// the native session, and the captured observation belongs to the old one.
var errGoalWriterExpired = errors.New("the captured goal write's publication expired")

// GoalWriterFor builds the goal writer of one captured publication. The caller
// captures the transcript when its native goal observation ARRIVES; the writer
// checks the sink, the publisher, and the native session fact atomically at
// each publication, so ordinary later turns in the same native session keep it
// valid and a context replacement refuses it.
func (sink *agentOutputSink) GoalWriterFor(captured agent.CapturedTranscript) (agent.CapturedGoalWriter, error) {
	if !sink.ownsGoal("GoalWriterFor") {
		return nil, errGoalWriterRefused
	}
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nil, errGoalWriterRefused
	}
	owner, ok := content.Publication.Owner().(*transcriptOwner)
	if !ok || owner == nil {
		return nil, errGoalWriterRefused
	}
	if owner.sink != sink {
		return nil, errGoalWriterRefused
	}
	// The expected session must equal the CAPTURED NATIVE SESSION FACT's id:
	// the owner's sessionID field only echoes the request, so comparing
	// against it would accept a capture from a foreign session that is born
	// expired instead of refused.
	if content.AgentSessionID == "" || owner.session == nil || content.AgentSessionID != owner.session.id {
		return nil, errGoalWriterRefused
	}
	return &capturedGoalWriter{operation: goalOperation{
		admission: &goalAdmission{
			h:         sink.h,
			agentID:   sink.agentID,
			sink:      sink,
			publisher: sink.turnPublisherSink(),
			session:   owner.session,
		},
		content: content.Clone(),
	}}, nil
}

// capturedGoalWriter applies goal writes under one observation-time authority.
type capturedGoalWriter struct {
	operation goalOperation
}

func (writer *capturedGoalWriter) UpsertGoal(update agent.GoalUpdate) error {
	if !writer.operation.admission.isCurrent() {
		return errGoalWriterExpired
	}
	return writer.apply(goalChange{update: cloneGoalUpdate(update).Clean()})
}

func (writer *capturedGoalWriter) ClearGoal() error {
	if !writer.operation.admission.isCurrent() {
		return errGoalWriterExpired
	}
	return writer.apply(goalChange{})
}

func (writer *capturedGoalWriter) apply(change goalChange) error {
	failure := writer.operation.admission.h.applyGoalUpdate(writer.operation, change)
	if failure.err != nil {
		return failure.err
	}
	return nil
}

func (operation goalOperation) captureNotification(payload map[string]interface{}) (agent.CapturedTranscript, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return agent.CapturedTranscript{}, err
	}
	content := operation.content.Clone()
	content.Original = data
	return agent.CaptureTranscript(operation.admission.sink, content, agent.SpanInfo{}), nil
}

// currentLocked compares only the original process sink and sole native-session fact.
// The caller holds the existing root mutation read lease.
func (admission *goalAdmission) currentLocked() bool {
	if admission.sink == nil {
		_, registered := admission.h.rootSinks.Load(admission.agentID)
		return !registered && admission.h.sinkForAgent(admission.agentID) == nil
	}
	return admission.sink.registeredCurrent() && admission.sink.turnPublisherSink() == admission.publisher &&
		admission.sink.currentMessageSessionFact() == admission.session
}

func (admission *goalAdmission) mutationMutex() *sync.RWMutex {
	if admission.sink != nil {
		return admission.h.transcriptMutationMutex(admission.sink.rootAgentID)
	}
	return admission.h.transcriptMutationMutex(admission.agentID)
}

func (admission *goalAdmission) isCurrent() bool {
	mutation := admission.mutationMutex()
	mutation.RLock()
	defer mutation.RUnlock()
	return admission.currentLocked()
}

func (admission *goalAdmission) readGoal() (db.GetAgentGoalRow, bool, error) {
	mutation := admission.mutationMutex()
	mutation.RLock()
	defer mutation.RUnlock()
	if !admission.currentLocked() {
		return db.GetAgentGoalRow{}, false, nil
	}
	goal := admission.h.goalMutex(admission.agentID)
	goal.Lock()
	defer goal.Unlock()
	row, err := admission.h.queries.GetAgentGoal(bgCtx(), admission.agentID)
	return row, true, err
}

type goalPreparation struct {
	previous      db.GetAgentGoalRow
	next          db.GetAgentGoalRow
	write         bool
	clear         bool
	clearProgress bool
	publication   goalPublication
	// A present pointer selects a capability projection, including an empty action list.
	capabilities *[]leapmuxv1.AgentGoalAction
}

type goalCommitResult uint8

const (
	goalExpired goalCommitResult = iota
	goalPrepareAgain
	goalCommitted
)

type goalFailure struct {
	message string
	err     error
}

// logGoalFailure receives local failure data after every mutation and publication lock releases.
func logGoalFailure(agentID string, failure goalFailure) {
	if failure.err != nil {
		slog.Warn(failure.message, "agent_id", agentID, "error", failure.err)
	}
}

// commitGoalPreparation repeats exact admission and compares every stored preparation input.
// The goal mutex orders the storage decision and immutable queue admission together.
func (h *OutputHandler) commitGoalPreparation(admission *goalAdmission, prepared goalPreparation, readFailure string) (goalCommitResult, goalFailure) {
	mutation := admission.mutationMutex()
	mutation.RLock()
	defer mutation.RUnlock()
	if !admission.currentLocked() {
		return goalExpired, goalFailure{}
	}
	goal := h.goalMutex(admission.agentID)
	goal.Lock()
	defer goal.Unlock()
	row, err := h.queries.GetAgentGoal(bgCtx(), admission.agentID)
	if err != nil {
		return goalExpired, goalFailure{message: readFailure, err: err}
	}
	if !sameGoalPreparationInputs(row, prepared.previous) {
		return goalPrepareAgain, goalFailure{}
	}
	if prepared.write {
		failureMessage := "failed to update agent goal"
		if prepared.clear {
			failureMessage = "failed to clear agent goal"
			err = h.queries.ClearAgentGoal(bgCtx(), db.ClearAgentGoalParams{
				ID:            admission.agentID,
				GoalUpdatedAt: prepared.next.GoalUpdatedAt,
			})
		} else {
			err = h.queries.UpdateAgentGoal(bgCtx(), db.UpdateAgentGoalParams{
				ID:               admission.agentID,
				GoalNativeID:     prepared.next.GoalNativeID,
				GoalObjective:    prepared.next.GoalObjective,
				GoalStatus:       prepared.next.GoalStatus,
				GoalStatusDetail: prepared.next.GoalStatusDetail,
				GoalCreatedAt:    prepared.next.GoalCreatedAt,
				GoalUpdatedAt:    prepared.next.GoalUpdatedAt,
			})
		}
		if err != nil {
			return goalExpired, goalFailure{message: failureMessage, err: err}
		}
		if prepared.clearProgress && admission.sink != nil {
			// An accepted replacement or clear invalidates the previous goal's cached counters.
			// Keep this removal with the stored goal decision, before any publication enters the queue.
			admission.sink.sessionInfoMu.Lock()
			delete(admission.sink.lastSessionInfo, contracts.SessionInfoKeyGoalProgress)
			admission.sink.sessionInfoMu.Unlock()
		}
	}
	if prepared.capabilities != nil {
		// AgentAlive reads the Manager map only. It calls no provider or lifecycle lock.
		// Project liveness under the goal mutex so an earlier read cannot publish after a later exit projection.
		prepared.publication.goal = GoalChangedEvent(admission.agentID, h.GoalSnapshotFrom(goalColumnsOfRow(row)), *prepared.capabilities)
	}
	h.enqueueGoalPublication(admission.agentID, prepared.publication)
	return goalCommitted, goalFailure{}
}

func sameGoalPreparationInputs(left, right db.GetAgentGoalRow) bool {
	return left.ID == right.ID && left.ParentAgentID == right.ParentAgentID &&
		left.GoalNativeID == right.GoalNativeID && left.GoalObjective == right.GoalObjective &&
		left.GoalStatus == right.GoalStatus && left.GoalStatusDetail == right.GoalStatusDetail &&
		sameGoalTime(left.GoalCreatedAt, right.GoalCreatedAt) && sameGoalTime(left.GoalUpdatedAt, right.GoalUpdatedAt)
}

func sameGoalTime(left, right sqltime.SQLiteNullTime) bool {
	return left.Valid == right.Valid && (!left.Valid || left.Time.Equal(right.Time))
}

// enqueueEvent admits typed immutable data under the existing root read lease.
// The caller drains watchers after this method releases the lease.
func (admission *goalAdmission) enqueueEvent(event *leapmuxv1.AgentEvent) bool {
	mutation := admission.mutationMutex()
	mutation.RLock()
	defer mutation.RUnlock()
	if !admission.currentLocked() {
		return false
	}
	admission.h.watcher.EnqueueAgentEvent(admission.agentID, event)
	return true
}
