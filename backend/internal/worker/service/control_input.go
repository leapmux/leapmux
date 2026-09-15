package service

import (
	"database/sql"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// notReadyInput refuses a dispatch that nothing delivered and that a later state
// change can still deliver. The queue requeues the item and pauses, so the item
// keeps its place and its content instead of failing.
//
// The pause it produces only the reader's own resume lifts -- no path resumes it
// automatically. So this trades a per-item Retry for a whole-queue Resume; it does
// not remove the click.
func notReadyInput(err error) error {
	return &inputqueue.DeliveryError{Err: err, Outcome: inputqueue.DispatchNotReady}
}

// storeFault reports a read of the WORKER'S OWN store that failed for a reason
// which says nothing about this input. Such a read never reached the provider, so
// the input is as undelivered as it was before, and failing it permanently makes
// the reader retype what a retry would have sent.
//
// A missing row is NOT one. The agents row cascades its queue items and its queue
// state, so sql.ErrNoRows from an agent read means the item's own agent is gone,
// and no later read brings it back; treating that as transient would pause the
// queue forever. The background-task row is the case that makes the exclusion
// load-bearing: it can go while the child agents row survives.
func storeFault(err error) bool {
	return err != nil && !errors.Is(err, sql.ErrNoRows)
}

// notReadyStoreFault is notReadyInput for a store fault, with the pause sentence
// that states what actually happened. AGENT_STOPPED would tell the reader the
// agent stopped, which is false for a database error and is the sentence the
// pause banner prints.
func notReadyStoreFault(err error) error {
	return &inputqueue.DeliveryError{
		Err:         err,
		Outcome:     inputqueue.DispatchNotReady,
		PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT,
	}
}

type controlInput struct {
	text   string
	source *db.GetControlResponseSourcesForInputRow
}

// resolveControlInput validates every approval-linked input before dispatch.
// Feedback receives a fixed prefix so its text cannot become a native slash command.
func (svc *Service) resolveControlInput(item inputqueue.DispatchItem, currentAgent db.Agent) (controlInput, error) {
	resolved := controlInput{text: item.Text}
	sources, err := svc.Queries.GetControlResponseSourcesForInput(bgCtx(), db.GetControlResponseSourcesForInputParams{
		AgentID: item.AgentID, InputID: item.ID,
	})
	if err != nil {
		// A missing row gives an empty slice here, so every error is a store
		// fault rather than an answer about this input. Nothing reached the
		// provider, and the next read can succeed. notReadyInput leaves the pause
		// reason unset, which recordDispatchFailure then answers with AGENT_STOPPED
		// -- a false sentence about a database error, on the very read this
		// classification exists for.
		return controlInput{}, notReadyStoreFault(fmt.Errorf("read the control responses for this input: %w", err))
	}
	if len(sources) > 1 {
		return controlInput{}, errors.New("the input matches more than one control response")
	}
	if len(sources) == 1 {
		source := &sources[0]
		if source.State != storedStateCompleted {
			// The answer row exists and is still on its way to COMPLETED.
			// enqueuePlanExecution inserts the item BEFORE processControlResponse
			// records the delivery, so the queue can drain it first. The
			// NotifyDependencyReady that finalizeControlResponse sends resumes
			// the queue, which a FAILED item would not wait for.
			return controlInput{}, notReadyInput(errors.New("the input's approval is not recorded yet"))
		}
		resolved.source = source
		if err := resolved.validateSession(currentAgent); err != nil {
			return controlInput{}, err
		}
	}
	if item.PrepareContext && item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION && resolved.source == nil {
		return controlInput{}, errors.New("the context replacement has no recorded approval")
	}
	if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK {
		resolved.text = "The user rejected the request. Their feedback follows:\n\n" + item.Text
	}
	return resolved, nil
}

func (input controlInput) validateSession(current db.Agent) error {
	if input.source != nil && (input.sessionID() == "" || input.sessionID() != current.AgentSessionID || input.source.AgentProvider != current.AgentProvider) {
		return errors.New("the original provider session is no longer active")
	}
	return nil
}

func (input controlInput) sessionID() string {
	if input.source == nil {
		return ""
	}
	if input.source.ExecutionSessionID != "" {
		return input.source.ExecutionSessionID
	}
	return input.source.AgentSessionID
}

func (svc *Service) sendResolvedInput(agentID string, input controlInput, attachments []*leapmuxv1.Attachment) error {
	if input.source == nil {
		return svc.Agents.SendInput(agentID, input.text, attachments)
	}
	provider, release := svc.Agents.LockProvider(agentID)
	// Only the READ is classified. validateSession's answer is about this input --
	// the original provider session is gone -- and stays a permanent failure.
	current, err := svc.queueAgentRow(agentID)
	if err == nil {
		err = input.validateSession(current)
	}
	release()
	if err != nil {
		return err
	}
	if provider == nil {
		return agent.ErrAgentNotFound
	}
	return agent.SendInputToSession(provider, input.sessionID(), input.text, attachments)
}
