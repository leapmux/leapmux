package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
)

// deliverRawInputToWorker sends raw NDJSON bytes directly to the worker agent's stdin,
// bypassing the UserInputMessage wrapper. Used for synthetic messages like plan mode toggles.
// Returns agentNotFound=true when the worker reports that the agent process does not exist.
func (s *AgentService) deliverRawInputToWorker(ctx context.Context, workerID, workspaceID, agentID string, content []byte) (agentNotFound bool, err error) {
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("worker is offline"))
	}

	ackResp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentRawInput{
			AgentRawInput: &leapmuxv1.AgentRawInput{
				WorkspaceId: workspaceID,
				AgentId:     agentID,
				Content:     content,
			},
		},
	})
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("send raw input: %w", err))
	}

	ack := ackResp.GetAgentInputAck()
	if ack.GetError() == leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND {
		return true, connect.NewError(connect.CodeInternal, fmt.Errorf("raw input failed: %s", ack.GetErrorReason()))
	}
	if ack.GetError() != leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_UNSPECIFIED {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("raw input failed: %s", ack.GetErrorReason()))
	}

	return false, nil
}

// deliverMessageToWorker sends a user message to the worker agent.
// Returns agentNotFound=true when the worker reports that the agent process does not exist.
// The caller is responsible for persisting delivery errors.
func (s *AgentService) deliverMessageToWorker(ctx context.Context, workerID, workspaceID, agentID, content string) (agentNotFound bool, err error) {
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("worker is offline"))
	}

	ackResp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentInput{
			AgentInput: &leapmuxv1.AgentInput{
				WorkspaceId: workspaceID,
				AgentId:     agentID,
				Content:     []byte(content),
			},
		},
	})
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("send agent input: %w", err))
	}

	ack := ackResp.GetAgentInputAck()
	if ack.GetError() == leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND {
		return true, connect.NewError(connect.CodeInternal, fmt.Errorf("agent input failed: %s", ack.GetErrorReason()))
	}
	if ack.GetError() != leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_UNSPECIFIED {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("agent input failed: %s", ack.GetErrorReason()))
	}

	return false, nil
}

// setDeliveryError persists a delivery error in DB and broadcasts it to watchers.
// Uses context.Background() for the DB write because the RPC context may have expired.
func (s *AgentService) setDeliveryError(ctx context.Context, agentID, msgID, errMsg string) {
	if dbErr := s.queries.SetMessageDeliveryError(ctx, db.SetMessageDeliveryErrorParams{
		DeliveryError: errMsg,
		ID:            msgID,
		AgentID:       agentID,
	}); dbErr != nil {
		slog.Error("persist delivery error", "agent_id", agentID, "msg_id", msgID, "error", dbErr)
	}
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_MessageError{
			MessageError: &leapmuxv1.AgentMessageError{
				AgentId:   agentID,
				MessageId: msgID,
				Error:     errMsg,
			},
		},
	})
}

// clearDeliveryError clears a delivery error in DB and broadcasts the cleared state.
func (s *AgentService) clearDeliveryError(ctx context.Context, agentID, msgID string) {
	s.setDeliveryError(ctx, agentID, msgID, "")
}
