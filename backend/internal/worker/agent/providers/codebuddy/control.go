package codebuddy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// sendInterruptControl stops the native child tasks and workflows also.
func (a *Agent) sendInterruptControl(sessionID string) error {
	body, err := json.Marshal(map[string]any{
		"subtype": "interrupt", "session_id": sessionID, "reason": "user",
	})
	if err != nil {
		return err
	}
	return a.sendControlFire(string(body))
}

// An SDK permission interrupt reports a permission error and no abort reason.
// Other failures keep their native outcome.
func codebuddyPermissionInterruptedResult(result resultMessage) bool {
	return result.IsError && len(result.Errors) == 1 &&
		strings.HasPrefix(result.Errors[0], "Permission denied for tool(s): ")
}

// codebuddyInterruptedPermissionReason is the reason of the refusal that an interrupt
// sends for a permission that waits. CodeBuddy hands it to the model as the result of
// the refused call.
const codebuddyInterruptedPermissionReason = "The user interrupted the turn."

func (a *Agent) hasOpenRootPermissionLocked() bool {
	for _, record := range a.controls {
		if record.rootPermission && !record.answering {
			return true
		}
	}
	return false
}

// refuseOpenPermissions refuses each permission that CodeBuddy still waits on, with a
// refusal that states the interrupt, and withdraws its card.
//
// CodeBuddy Code 2.160.0 stops the run when this refusal carries interrupt:true.
// A separate interrupt replaces the refusal with a generic rejection and resumes the model loop.
func (a *Agent) refuseOpenPermissions() (bool, error) {
	a.mu.Lock()
	requestIDs := make([]string, 0, len(a.controls))
	for requestID, record := range a.controls {
		if record.rootPermission && !record.answering {
			requestIDs = append(requestIDs, requestID)
		}
	}
	a.mu.Unlock()
	// Refuse in a stable order, so one interrupt always writes the same lines.
	slices.Sort(requestIDs)
	refused := false
	for _, requestID := range requestIDs {
		a.mu.Lock()
		record := a.controls[requestID]
		open := record != nil && record.rootPermission && !record.answering
		if open {
			record.answering = true
		}
		a.mu.Unlock()
		if !open {
			continue
		}
		frame, err := json.Marshal(map[string]any{
			"type": frameTypeControlResponse,
			"response": map[string]any{
				"subtype":    "success",
				"request_id": requestID,
				"response":   contracts.CodebuddyCanUseToolAnswer{Allowed: false, Reason: codebuddyInterruptedPermissionReason, Interrupt: true},
			},
		})
		if err != nil {
			slog.Error("codebuddy: encode the interrupt refusal", "agent_id", a.AgentID(), "error", err)
			return refused, err
		}
		err = a.Process.SendRawInput(frame, agent.StopContext{})
		a.finishControlWrite(requestID, record, err, true, false)
		if err != nil {
			return refused, err
		}
		refused = true
	}
	return refused, nil
}

// SendRawInput claims each control response before it writes to the native process.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	var frame struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Type != frameTypeControlResponse {
		return a.Process.SendRawInput(data, stop)
	}
	a.mu.Lock()
	record := a.controls[frame.Response.RequestID]
	a.mu.Unlock()
	if record == nil {
		return fmt.Errorf("the CodeBuddy control request is no longer pending")
	}
	select {
	case <-record.publicationDone:
	case <-a.Context().Done():
		return a.Context().Err()
	}
	claimed, err := a.writeControlAnswer(frame.Response.RequestID, record, data, stop, false)
	if claimed {
		a.finishControlWrite(frame.Response.RequestID, record, err, false, false)
	}
	return err
}
