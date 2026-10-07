package codebuddy

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
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

// rememberOpenPermission records a published can_use_tool request that CodeBuddy waits
// on, so an interrupt can refuse it (see refuseOpenPermissions).
func (a *Agent) rememberOpenPermission(requestID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openPermissions == nil {
		a.openPermissions = make(map[string]struct{})
	}
	a.openPermissions[requestID] = struct{}{}
}

// forgetOpenPermission drops a permission that CodeBuddy no longer waits on: the reader
// answered it, or the CLI withdrew it.
func (a *Agent) forgetOpenPermission(requestID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, open := a.openPermissions[requestID]
	delete(a.openPermissions, requestID)
	return open
}

// refuseOpenPermissions refuses each permission that CodeBuddy still waits on, with a
// refusal that states the interrupt, and withdraws its card.
//
// CodeBuddy Code 2.160.0 stops the run when this refusal carries interrupt:true.
// A separate interrupt replaces the refusal with a generic rejection and resumes the model loop.
func (a *Agent) refuseOpenPermissions() (bool, error) {
	a.mu.Lock()
	requestIDs := make([]string, 0, len(a.openPermissions))
	for requestID := range a.openPermissions {
		requestIDs = append(requestIDs, requestID)
	}
	a.mu.Unlock()
	// Refuse in a stable order, so one interrupt always writes the same lines.
	slices.Sort(requestIDs)
	refused := false
	for _, requestID := range requestIDs {
		a.mu.Lock()
		_, open := a.openPermissions[requestID]
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
		if err := a.Process.SendRawInput(frame); err != nil {
			return refused, err
		}
		refused = true
		// A native cancel or turn end can retire the request during the write.
		// A failed write retains only the requests that still wait.
		if a.forgetOpenPermission(requestID) {
			a.sink.CancelControlRequest(requestID)
		}
	}
	return refused, nil
}

// SendRawInput forwards one raw stdin frame. A control_response that answers a waiting
// permission retires it, so an interrupt does not refuse it again.
func (a *Agent) SendRawInput(data []byte) error {
	var frame struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &frame) == nil && frame.Type == frameTypeControlResponse && frame.Response.RequestID != "" {
		a.permissionWriteMu.Lock()
		defer a.permissionWriteMu.Unlock()
		if err := a.Process.SendRawInput(data); err != nil {
			return err
		}
		a.forgetOpenPermission(frame.Response.RequestID)
		return nil
	}
	return a.Process.SendRawInput(data)
}
