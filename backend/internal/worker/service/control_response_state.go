package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func controlResponseState(ctx context.Context, queries *db.Queries, agentID, requestID, claimToken string) (leapmuxv1.ControlResponseState, error) {
	answer, err := queries.GetControlResponseAnswer(ctx, db.GetControlResponseAnswerParams{
		AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
	})
	if err == nil {
		return controlDeliveryResponseState(answer.State)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, err
	}
	currentClaim, err := queries.GetControlRequestClaim(ctx, db.GetControlRequestClaimParams{AgentID: agentID, RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) || err == nil && currentClaim != claimToken {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, nil
	}
	if err != nil {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, err
	}
	return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, nil
}

// recordControlResponse finishes recording a response that the Worker already
// delivered for one request instance, and reports the state of the instance.
// It sends nothing to the agent. A request with no saved response only
// reports its state.
func (svc *Service) recordControlResponse(currentAgent db.Agent, request *leapmuxv1.RecordControlResponseRequest) (*leapmuxv1.RecordControlResponseResponse, error) {
	requestID, claimToken := request.GetRequestId(), request.GetClaimToken()
	if requestID == "" {
		return nil, errors.New("the control request ID is missing")
	}
	var operationErr error
	answer, err := svc.Queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
		AgentID: currentAgent.ID, RequestID: requestID, ClaimToken: claimToken,
	})
	if err == nil {
		operationErr = svc.resumeControlResponseFinalization(answer)
	} else if !errors.Is(err, sql.ErrNoRows) {
		operationErr = err
	}
	state, errText := svc.reportControlResponseState(currentAgent, requestID, claimToken, operationErr)
	return &leapmuxv1.RecordControlResponseResponse{State: state, Error: errText}, nil
}

// deliverControlAnswer delivers answer for one request instance and reports
// the state of the instance. The answer must address requestID, the request
// that AnswerControl read and the provider answered.
func (svc *Service) deliverControlAnswer(currentAgent db.Agent, requestID string, answer workerAnswer) (leapmuxv1.ControlResponseState, string, error) {
	if requestID == "" {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, "", errors.New("the control request ID is missing")
	}
	if decodedID := svc.Agents.Registry().Plugin(currentAgent.AgentProvider).ControlResponseRequestID(answer.content); decodedID != requestID {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, "", fmt.Errorf("the answer addresses the request %q, not %q", decodedID, requestID)
	}
	state, errText := svc.reportControlResponseState(currentAgent, requestID, answer.claimToken, svc.processControlAnswer(currentAgent, answer))
	return state, errText, nil
}

// reportControlResponseState reads the state of one request instance after
// an operation on it, joins the operation's failure with a failure to read
// the state, and publishes the state to every watcher.
//
// An answer to an instance that the provider or the Worker replaced
// (agent.ErrControlInstanceReplaced) is a silent refusal once that instance
// reads canceled, as processControlAnswer's refusal of a retired instance is.
// The reader's card of the instance is gone then, and the new instance has a
// card of its own, so no failure is left to show. While the instance still
// waits, the refusal keeps its text, because its card stays.
func (svc *Service) reportControlResponseState(currentAgent db.Agent, requestID, claimToken string, operationErr error) (leapmuxv1.ControlResponseState, string) {
	state, stateErr := controlResponseState(bgCtx(), svc.Queries, currentAgent.ID, requestID, claimToken)
	if stateErr == nil && state == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED && errors.Is(operationErr, agent.ErrControlInstanceReplaced) {
		operationErr = nil
	}
	var errText string
	if err := errors.Join(operationErr, stateErr); err != nil {
		errText = err.Error()
	}
	svc.broadcastControlResponseState(currentAgent, requestID, claimToken)
	return state, errText
}

// Publish current evidence only for the matching pending request instance.
func (svc *Service) broadcastControlResponseState(currentAgent db.Agent, requestID, claimToken string) {
	request, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{AgentID: currentAgent.ID, RequestID: requestID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Error("could not load the control request for its state update", "agent_id", currentAgent.ID, "request_id", requestID, "error", err)
		return
	}
	provider := currentAgent.AgentProvider
	if errors.Is(err, sql.ErrNoRows) || request.ClaimToken != claimToken {
		answer, answerErr := svc.Queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
			AgentID: currentAgent.ID, RequestID: requestID, ClaimToken: claimToken,
		})
		if errors.Is(answerErr, sql.ErrNoRows) {
			return
		}
		if answerErr != nil {
			slog.Error("could not load a response for its state update", "agent_id", currentAgent.ID, "request_id", requestID, "error", answerErr)
			return
		}
		if answer.State != storedStateDelivered {
			return
		}
		request = db.ControlRequest{RequestID: requestID, ClaimToken: claimToken, Payload: answer.RequestPayload, AgentSessionID: answer.AgentSessionID, SourceSeq: answer.SourceSeq, NonBlocking: answer.NonBlocking}
		provider = answer.AgentProvider
	}
	event, stateErr := buildAgentControlRequest(svc.Queries, svc.Agents.Registry().Plugin(provider), currentAgent.ID, provider, agent.ControlRequest{
		RequestID: requestID, Payload: request.Payload, SourceSeq: request.SourceSeq, AgentSessionID: request.AgentSessionID, NonBlocking: request.NonBlocking,
	}, claimToken)
	if stateErr != nil {
		slog.Error("could not read the control response state", "agent_id", currentAgent.ID, "request_id", requestID, "error", stateErr)
	}
	svc.Watchers.BroadcastAgentEvent(currentAgent.ID, &leapmuxv1.AgentEvent{
		AgentId: currentAgent.ID,
		Event:   &leapmuxv1.AgentEvent_ControlResponseChanged{ControlResponseChanged: event},
	})
}

func controlDeliveryResponseState(state leapmuxv1.ControlDeliveryState) (leapmuxv1.ControlResponseState, error) {
	switch state {
	case storedStatePending:
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING, nil
	case storedStateUncertain:
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN, nil
	case storedStateDelivered:
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, nil
	case storedStateCompleted:
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, nil
	default:
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, fmt.Errorf("unknown control delivery state %d", state)
	}
}
