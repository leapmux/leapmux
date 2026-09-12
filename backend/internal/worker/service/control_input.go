package service

import (
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

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
		return controlInput{}, err
	}
	if len(sources) > 1 {
		return controlInput{}, errors.New("the input matches more than one control response")
	}
	if len(sources) == 1 {
		source := &sources[0]
		if source.State != storedStateCompleted {
			return controlInput{}, errors.New("the input's approval is not recorded")
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
	if input.source != nil && (input.sessionID() == "" || input.sessionID() != current.AgentSessionID || input.source.AgentProvider != int64(current.AgentProvider)) {
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
