package service

import (
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// notReadyInput refuses a dispatch that nothing delivered and that a later state
// change can still deliver. The queue requeues the item and pauses, so the user
// never retries by hand what the Worker can resume on its own.
func notReadyInput(err error) error {
	return &inputqueue.DeliveryError{Err: err, Outcome: inputqueue.DispatchNotReady}
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
		// provider, and the next read can succeed.
		return controlInput{}, notReadyInput(fmt.Errorf("read the control responses for this input: %w", err))
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
	current, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
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
