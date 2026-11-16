package kimi

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A submission identifies its exact receiving agent and native request before
// HTTP starts. Native execution and a final receipt can precede the HTTP reply.
type kimiPromptSubmission struct {
	id, sessionID, receiver string
	observed                bool
	ready                   func(agent.CapturedTranscript) error
	readyCapture            agent.CapturedTranscript
	readyQueued             bool
}

func (a *Agent) reservePromptSubmission(sessionID, receiver string) (*kimiPromptSubmission, error) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	return a.registerPromptSubmissionLocked(sessionID, receiver)
}

func (a *Agent) registerPromptSubmissionLocked(sessionID, receiver string) (*kimiPromptSubmission, error) {
	a.Mu.Lock()
	current := a.sessionID == sessionID && !a.StoppedLocked()
	a.Mu.Unlock()
	if !current {
		return nil, errors.New("the Kimi Code session changed before prompt submission")
	}
	submission := &kimiPromptSubmission{id: id.Generate(), sessionID: sessionID, receiver: receiver}
	if a.promptSubmissions == nil {
		a.promptSubmissions = make(map[string]*kimiPromptSubmission)
	}
	a.promptSubmissions[submission.id] = submission
	return submission, nil
}

func (a *Agent) finishPromptSubmission(submission *kimiPromptSubmission, err error) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	if errors.Is(err, agent.ErrDeliveryUncertain) {
		return
	}
	a.releaseChildPromptSubmission(submission)
	if !submission.observed && a.promptSubmissions[submission.id] == submission {
		delete(a.promptSubmissions, submission.id)
	}
}

// The caller holds dispatchMu. Pending admission and native turn activity are
// independent; an unrelated user turn cannot consume this submission's slot.
func (a *Agent) releaseChildPromptSubmission(submission *kimiPromptSubmission) {
	if a.promptSubmissions[submission.id] != submission {
		return
	}
	a.Mu.Lock()
	current := a.sessionID == submission.sessionID
	a.Mu.Unlock()
	if !current {
		return
	}
	released := false
	a.children.update(submission.receiver, func(child *kimiChild) {
		if child.pendingPrompt == submission {
			child.pendingPrompt = nil
			released = true
		}
	})
	if released && submission.ready != nil && !submission.readyQueued {
		submission.readyQueued = true
		ready, captured := submission.ready, submission.readyCapture
		err := a.queueRegistryWrite(a.sink, "publish Kimi child input readiness", func(agent.BackgroundTaskServices) error { return ready(captured) })
		if agent.OutputRetained(err) {
			submission.ready = nil
			submission.readyCapture = agent.CapturedTranscript{}
		} else {
			submission.readyQueued = false
		}
	}
	if released && !submission.observed {
		run := a.run(submission.receiver)
		a.Mu.Lock()
		active := run.turnActive
		a.Mu.Unlock()
		if !active {
			// The readiness write precedes cleanup in the same output queue.
			a.queueChildCleanup(run)
		}
	}
}

func (a *Agent) ownedPrompt(run *kimiRun, sessionID, promptID string) *kimiPromptSubmission {
	submission := a.promptSubmissions[promptID]
	if submission == nil || submission.sessionID != sessionID || submission.receiver != run.agentID {
		return nil
	}
	return submission
}

func (a *Agent) nativeTranscriptSession(sessionID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return sessionID != "" && sessionID == a.sessionID
}

func (a *Agent) observePromptTrigger(run *kimiRun, turn *kimiTranscriptTurn, sessionID, promptID string) bool {
	if !a.nativeTranscriptSession(sessionID) || turn.triggerPromptID != "" && promptID != "" && turn.triggerPromptID != promptID {
		return false
	}
	if promptID != "" {
		turn.triggerPromptID = promptID
	}
	if submission := a.ownedPrompt(run, sessionID, promptID); submission != nil {
		submission.observed = true
		a.releaseChildPromptSubmission(submission)
	}
	return true
}

// Snapshot identity validation precedes every state change, including control reconciliation.
func (a *Agent) validateTranscriptReplacement(run *kimiRun, snapshot *kimiTranscriptSnapshot) error {
	if err := validateKimiTranscriptSnapshot(snapshot); err != nil {
		return err
	}
	for _, raw := range snapshot.Items {
		var turn struct {
			Kind, TurnID    string
			TriggerPromptID string `json:"triggerPromptId"`
		}
		if err := json.Unmarshal(raw, &turn); err != nil {
			return err
		}
		if turn.Kind != contracts.KimiTranscriptNodeTurn {
			continue
		}
		if prior := run.transcript.turns[turn.TurnID]; prior != nil && prior.triggerPromptID != "" && turn.TriggerPromptID != "" && prior.triggerPromptID != turn.TriggerPromptID {
			return fmt.Errorf("the Kimi snapshot changes the trigger of turn %q", turn.TurnID)
		}
	}
	return nil
}

func (a *Agent) observeConsumedPrompts(run *kimiRun, sessionID, turnID, stepID string, raw json.RawMessage) {
	var frame struct {
		Kind, Role string
		PromptIDs  []string `json:"promptIds"`
	}
	if !a.nativeTranscriptSession(sessionID) || json.Unmarshal(raw, &frame) != nil || frame.Kind != contracts.KimiTranscriptFrameText || frame.Role != contracts.KimiTranscriptRoleUser {
		return
	}
	turn := run.transcript.turn(turnID)
	for _, promptID := range frame.PromptIDs {
		if submission := a.ownedPrompt(run, sessionID, promptID); submission != nil {
			submission.observed = true
			a.releaseChildPromptSubmission(submission)
			if turn.consumedPrompts == nil {
				turn.consumedPrompts = make(map[string]map[string]struct{})
			}
			if turn.consumedPrompts[stepID] == nil {
				turn.consumedPrompts[stepID] = make(map[string]struct{})
			}
			turn.consumedPrompts[stepID][promptID] = struct{}{}
		}
	}
}

type kimiPromptRecord struct {
	PromptID  string `json:"promptId"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

func decodeKimiPromptRecord(raw json.RawMessage) (kimiPromptRecord, bool) {
	var record kimiPromptRecord
	if json.Unmarshal(raw, &record) != nil || kimiCheckID("prompt", record.PromptID) != nil || record.CreatedAt == "" {
		return record, false
	}
	switch record.Status {
	case kimiPromptRunning, kimiPromptQueued, kimiPromptBlocked, kimiPromptCompleted, kimiPromptFailed, kimiPromptAborted:
		return record, true
	default:
		return record, false
	}
}

func (a *Agent) applyNativePromptRecord(run *kimiRun, sessionID string, raw json.RawMessage) {
	if !a.nativeTranscriptSession(sessionID) {
		return
	}
	record, valid := decodeKimiPromptRecord(raw)
	if !valid {
		return
	}
	submission := a.ownedPrompt(run, sessionID, record.PromptID)
	if submission == nil {
		return
	}
	switch record.Status {
	case kimiPromptBlocked, kimiPromptCompleted, kimiPromptFailed, kimiPromptAborted:
		a.releaseChildPromptSubmission(submission)
	}
}
