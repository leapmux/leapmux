package kimi

import (
	"errors"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A submission identifies its exact receiving agent and native request before
// HTTP starts. Native execution and a final receipt can precede the HTTP reply.
type kimiPromptSubmission struct {
	id, sessionID, receiver string
	observed                bool
	tracked                 bool
	turnID, itemID          string
	consumingOpener         bool
	conflictingConsumption  bool
	ready                   func(agent.CapturedTranscript) error
	readyCapture            agent.CapturedTranscript
}

func (a *Agent) reservePromptSubmission(sessionID, receiver string, tracked bool) (*kimiPromptSubmission, error) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	submission, err := a.registerPromptSubmissionLocked(sessionID, receiver)
	if err == nil {
		submission.tracked = tracked
	}
	return submission, err
}

func (submission *kimiPromptSubmission) receipt() agent.NativeInputReceipt {
	return agent.NativeInputReceipt{SessionID: submission.sessionID, InputID: submission.id}
}

func (a *Agent) hasObservedPrompt(submission *kimiPromptSubmission) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	return submission.observed && !submission.conflictingConsumption
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
	if !released {
		return
	}
	run := a.run(submission.receiver)
	if submission.ready != nil {
		run.readyPromptSubmissions = append(run.readyPromptSubmissions, submission)
		a.queueChildPromptReadiness(run)
	}
	if !submission.observed {
		a.Mu.Lock()
		active := a.inputStateLocked(run.agentID).Active
		a.Mu.Unlock()
		if !active {
			// The readiness write precedes cleanup in the same output queue.
			a.queueChildCleanup(run)
		}
	}
}

// The prompt item consumes admission while its native turn is still active.
// Wake the next input only once the receiving child can accept it. The writer
// capture enters the ordered queue before cleanup can retire that lifetime.
// The caller holds dispatchMu.
func (a *Agent) queueChildPromptReadiness(run *kimiRun) {
	if len(run.readyPromptSubmissions) == 0 {
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	current := a.runs[run.agentID] == run && !a.StoppedLocked() && !a.ProcessExitedLocked()
	active := a.inputStateLocked(run.agentID).Active
	a.Mu.Unlock()
	if current && active {
		return
	}
	waiting := run.readyPromptSubmissions[:0]
	for _, submission := range run.readyPromptSubmissions {
		if current && submission.sessionID == sessionID {
			ready, captured := submission.ready, submission.readyCapture
			err := a.queueRegistryWrite(a.sink, "publish Kimi child input readiness", func(agent.BackgroundTaskServices) error { return ready(captured) })
			if !agent.OutputRetained(err) {
				waiting = append(waiting, submission)
				continue
			}
		}
		submission.ready = nil
		submission.readyCapture = agent.CapturedTranscript{}
	}
	clear(run.readyPromptSubmissions[len(waiting):])
	run.readyPromptSubmissions = waiting
}

func (a *Agent) ownedPrompt(run *kimiRun, sessionID, promptID string) *kimiPromptSubmission {
	submission := a.promptSubmissions[promptID]
	if submission == nil || submission.sessionID != sessionID || submission.receiver != run.agentID {
		return nil
	}
	return submission
}
