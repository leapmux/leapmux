package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (s *kimiStream) sendTaskCommand(ctx context.Context, operation, sessionID, ownerAgentID, taskID string) error {
	_, err := s.requestTask(ctx, operation, sessionID, ownerAgentID, taskID)
	return err
}

// readTaskState removes the output watch even when its response is invalid or late.
func (s *kimiStream) readTaskState(ctx context.Context, sessionID string, address kimiTaskAddress) (state kimiNativeTaskState, resultErr error) {
	for _, target := range []struct{ kind, id string }{{"session", sessionID}, {"agent", address.ownerAgentID}, {"task", address.taskID}} {
		if err := kimiCheckID(target.kind, target.id); err != nil {
			return state, err
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), kimiStreamWriteTimeout)
		defer cancel()
		if err := s.sendTaskCommand(cleanup, kimiFrameTaskUnwatch, sessionID, address.ownerAgentID, address.taskID); err != nil {
			slog.Warn("unwatch the Kimi task after state read", "session_id", sessionID, "agent_id", address.ownerAgentID, "task_id", address.taskID, "error", err)
		}
	}()
	response, err := s.requestTask(ctx, kimiFrameTaskWatch, sessionID, address.ownerAgentID, address.taskID)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(response.Task, &state); err != nil {
		return kimiNativeTaskState{}, fmt.Errorf("the Kimi task response has no matching native state: %w", err)
	}
	if state.TaskID != address.taskID {
		return kimiNativeTaskState{}, errors.New("the Kimi task response has no matching native state")
	}
	return state, nil
}

func (s *kimiStream) requestTask(ctx context.Context, operation, sessionID, ownerAgentID, taskID string) (kimiFrame, error) {
	if err := ctx.Err(); err != nil {
		return kimiFrame{}, err
	}
	switch operation {
	case kimiFrameTaskStop, kimiFrameTaskWatch, kimiFrameTaskUnwatch:
	default:
		return kimiFrame{}, fmt.Errorf("unsupported Kimi task operation %q", operation)
	}
	for _, target := range []struct{ kind, id string }{{"session", sessionID}, {"agent", ownerAgentID}, {"task", taskID}} {
		if err := kimiCheckID(target.kind, target.id); err != nil {
			return kimiFrame{}, err
		}
	}
	response, err := s.requestFrame(ctx, kimiFrameRequest{operation: "task", build: func(id string) any {
		return map[string]any{"type": operation, "request_id": id, "session_id": sessionID, "agent_id": ownerAgentID, "task_id": taskID}
	}})
	if err != nil {
		return kimiFrame{}, err
	}
	if response.Type != kimiFrameTaskResponse {
		return response, fmt.Errorf("%w: the Kimi server sent an invalid task response", agent.ErrDeliveryUncertain)
	}
	if response.Code != kimiCodeOK {
		return response, fmt.Errorf("the Kimi server refused the task command: %s (code %d)", response.Msg, response.Code)
	}
	return response, nil
}
