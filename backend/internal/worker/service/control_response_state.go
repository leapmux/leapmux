package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func controlResponseState(queries *db.Queries, agentID, requestID, claimToken string) (leapmuxv1.ControlResponseState, error) {
	answer, err := queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
		AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
	})
	if err == nil {
		return controlDeliveryResponseState(answer.State)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, err
	}
	request, err := queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) || err == nil && request.ClaimToken != claimToken {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, nil
	}
	if err != nil {
		return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, err
	}
	return leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, nil
}

func (svc *Service) respondToControlRequest(currentAgent db.Agent, request *leapmuxv1.SendControlResponseRequest) (*leapmuxv1.SendControlResponseResponse, error) {
	requestID := request.GetRequestId()
	if !request.GetRecordOnly() {
		decodedID := svc.Agents.Registry().Plugin(currentAgent.AgentProvider).ControlResponseRequestID(request.GetContent())
		if requestID != "" && requestID != decodedID {
			return nil, errors.New("the response does not match the request ID")
		}
		requestID = decodedID
	} else if len(request.GetContent()) != 0 || request.GetPlanApproval() != nil {
		return nil, errors.New("a recording request cannot contain a new response or plan settings")
	}
	if requestID == "" {
		return nil, errors.New("the control request ID is missing")
	}
	var operationErr error
	if request.GetRecordOnly() {
		answer, err := svc.Queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
			AgentID: currentAgent.ID, RequestID: requestID, ClaimToken: request.GetClaimToken(),
		})
		if err == nil {
			operationErr = svc.resumeControlResponseFinalization(answer)
		} else if !errors.Is(err, sql.ErrNoRows) {
			operationErr = err
		}
	} else {
		operationErr = svc.processControlResponse(currentAgent, request)
	}
	state, stateErr := controlResponseState(svc.Queries, currentAgent.ID, requestID, request.GetClaimToken())
	response := &leapmuxv1.SendControlResponseResponse{State: state}
	if err := errors.Join(operationErr, stateErr); err != nil {
		response.Error = err.Error()
	}
	svc.broadcastControlResponseState(currentAgent, requestID, request.GetClaimToken())
	return response, nil
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
		request = db.ControlRequest{RequestID: requestID, ClaimToken: claimToken, Payload: answer.RequestPayload, AgentSessionID: answer.AgentSessionID, SourceSeq: answer.SourceSeq}
		provider = answer.AgentProvider
	}
	event, stateErr := buildAgentControlRequest(svc.Queries, currentAgent.ID, provider, agent.ControlRequest{
		RequestID: requestID, Payload: request.Payload, SourceSeq: request.SourceSeq, AgentSessionID: request.AgentSessionID,
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
