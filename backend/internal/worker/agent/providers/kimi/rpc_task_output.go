package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

const kimiTaskOutputTailBytes = 1 << 20

type kimiTaskOutput struct {
	offset int64
	text   string
}

// readTaskOutput owns a connection because a task has one watch slot per connection.
// Closing it disposes its watch without withdrawing another reader's watch.
func (s *kimiStream) readTaskOutput(ctx context.Context, sessionID string, address kimiTaskAddress) (kimiTaskOutput, error) {
	for _, target := range []struct{ kind, id string }{{"session", sessionID}, {"agent", address.ownerAgentID}, {"task", address.taskID}} {
		if err := kimiCheckID(target.kind, target.id); err != nil {
			return kimiTaskOutput{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return kimiTaskOutput{}, err
	}
	conn, err := s.dial(ctx, kimiStreamWriteTimeout)
	if err != nil {
		return kimiTaskOutput{}, err
	}
	defer func() { _ = conn.CloseNow() }()
	const requestID = "task-report"
	request, err := json.Marshal(map[string]any{"type": kimiFrameTaskWatch, "request_id": requestID, "session_id": sessionID,
		"agent_id": address.ownerAgentID, "task_id": address.taskID, "tail_bytes": kimiTaskOutputTailBytes, "since_offset": 0})
	if err != nil {
		return kimiTaskOutput{}, err
	}
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		return kimiTaskOutput{}, err
	}
	var output kimiTaskOutput
	acknowledged, received := false, false
	for !acknowledged || !received {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return kimiTaskOutput{}, err
		}
		var frame kimiFrame
		if json.Unmarshal(raw, &frame) != nil {
			return kimiTaskOutput{}, errors.New("the Kimi task connection supplied an invalid frame")
		}
		switch frame.Type {
		case kimiFramePing:
			s.pong(ctx, conn, frame)
		case kimiFrameTaskResponse:
			if frame.RequestID != requestID {
				continue
			}
			if frame.Code != kimiCodeOK {
				return kimiTaskOutput{}, fmt.Errorf("the Kimi server refused the task output read: %s (code %d)", frame.Msg, frame.Code)
			}
			var state kimiNativeTaskState
			if json.Unmarshal(frame.Task, &state) != nil || state.TaskID != address.taskID || state.Kind != "subagent" || state.Status != "ended" {
				return kimiTaskOutput{}, errors.New("the Kimi task output response has no matching finished task")
			}
			if _, ended := kimiTaskStatus(state.Outcome); !ended {
				return kimiTaskOutput{}, errors.New("the Kimi task output response has no final outcome")
			}
			acknowledged = true
		case "task.output":
			var chunk struct {
				SessionID string  `json:"session_id"`
				AgentID   string  `json:"agent_id"`
				TaskID    string  `json:"task_id"`
				Offset    *int64  `json:"offset"`
				Text      *string `json:"text"`
			}
			if json.Unmarshal(raw, &chunk) != nil {
				return kimiTaskOutput{}, errors.New("the Kimi task output has an invalid shape")
			}
			if chunk.SessionID != sessionID || chunk.AgentID != address.ownerAgentID || chunk.TaskID != address.taskID {
				continue
			}
			if chunk.Offset == nil || *chunk.Offset < 0 || chunk.Text == nil || len(*chunk.Text) > kimiTaskOutputTailBytes {
				return kimiTaskOutput{}, errors.New("the Kimi task output has an invalid byte position or size")
			}
			if !received {
				output = kimiTaskOutput{offset: *chunk.Offset, text: *chunk.Text}
				received = true
			}
		}
	}
	return output, nil
}
