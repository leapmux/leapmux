package codebuddy

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// readOutputLoop reads NDJSON frames from stdout until the process exits.
func (a *Agent) readOutputLoop(scanner *bufio.Scanner) {
	a.ReadOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
}

// handleOutput dispatches one NDJSON frame.
func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	switch line.Type {
	case contracts.CodebuddyFrameKindSystem:
		a.handleSystem(line.Raw)
	case contracts.CodebuddyFrameKindAssistant:
		a.handleAssistant(line.Raw)
	case contracts.CodebuddyFrameKindResult:
		a.handleResult(line.Raw)
	case frameTypeControlRequest:
		a.handleInboundControlRequest(line.Raw)
	case string(MessageTypeControlCancelRequest):
		a.handleInboundControlCancel(line.Raw)
	case contracts.CodebuddyFrameKindConversationReset:
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, line.Raw); err != nil {
			slog.Debug("codebuddy: persist notification", "agent_id", a.AgentID(), "error", err)
		}
	default:
		// user, tool_progress, active_goal, control_notification, error: forward
		// verbatim so the browser plugin can classify and draw them.
		a.persistRaw(line.Raw)
	}
}

// handleSystem reads the init frame and publishes the rest verbatim.
func (a *Agent) handleSystem(raw []byte) {
	var envelope struct {
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Subtype == contracts.CodebuddySystemSubtypeInit {
		var init systemInitMessage
		if json.Unmarshal(raw, &init) == nil {
			a.mu.Lock()
			if init.SessionID != "" {
				a.sessionID = init.SessionID
			}
			if init.Model != "" {
				a.model = init.Model
			}
			if init.PermissionMode != "" {
				a.permissionMode = init.PermissionMode
			}
			sessionID := a.sessionID
			a.mu.Unlock()
			if sessionID != "" {
				a.sink.UpdateSessionID(sessionID)
			}
		}
	}
	a.persistRaw(raw)
}

// handleAssistant publishes the frame verbatim and arms the turn from output.
func (a *Agent) handleAssistant(raw []byte) {
	var msg assistantMessage
	if json.Unmarshal(raw, &msg) == nil {
		a.mu.Lock()
		if msg.SessionID != "" && a.sessionID == "" {
			a.sessionID = msg.SessionID
		}
		a.mu.Unlock()
	}
	a.armTurnFromOutput()
	a.persistRaw(raw)
}

// handleResult ends the turn and publishes the frame verbatim. A `result` ends
// a turn and NOT the process: CodeBuddy may still push background
// task_notification frames after it, so the reader keeps going.
func (a *Agent) handleResult(raw []byte) {
	a.setTurnActive(false)
	a.persistRaw(raw)
}

// armTurnFromOutput marks the turn active when a frame arrives while the agent
// believes it is idle. The CLI emits output only for work it is doing, so the
// first frame of a turn is enough to repair a missed transition.
func (a *Agent) armTurnFromOutput() {
	a.mu.Lock()
	wasActive := a.active
	a.mu.Unlock()
	if !wasActive {
		a.setTurnActive(true)
	}
}

// handleInboundControlRequest publishes an outbound control_request
// (can_use_tool, hook_callback, elicitation_create) so the worker can raise a
// control banner and answer it. The publish is what raises the banner: the
// transcript copy alone never reaches the hub's control-request store, and the
// CLI then waits on a request nobody can answer until stop cancels it.
func (a *Agent) handleInboundControlRequest(raw []byte) {
	var envelope controlRequestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		slog.Warn("codebuddy: malformed control_request", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.persistRaw(raw)
	if err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: envelope.RequestID, Payload: raw}); err != nil {
		slog.Error("codebuddy: publish control request", "agent_id", a.AgentID(), "request_id", envelope.RequestID, "error", err)
		response, marshalErr := json.Marshal(map[string]any{
			"type":     frameTypeControlResponse,
			"response": map[string]any{"subtype": "error", "request_id": envelope.RequestID, "error": providerkit.ControlPublicationFailure},
		})
		if marshalErr != nil {
			slog.Error("codebuddy: encode control failure", "agent_id", a.AgentID(), "error", marshalErr)
			return
		}
		if err := a.SendRawInput(response); err != nil {
			slog.Warn("codebuddy: send control failure", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// handleInboundControlCancel retires the hub request a control_cancel_request
// withdraws, so a cancelled can_use_tool does not leave a banner open.
func (a *Agent) handleInboundControlCancel(raw []byte) {
	var envelope struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		slog.Warn("codebuddy: malformed control_cancel_request", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.persistRaw(raw)
	a.sink.CancelControlRequest(envelope.RequestID)
}

// handlePendingControlResponse intercepts a control_response that answers one
// of this agent's own pending requests. Returns true when consumed.
func (a *Agent) handlePendingControlResponse(line *providerkit.ParsedLine) bool {
	if line.Type != frameTypeControlResponse {
		return false
	}
	var envelope controlResponseEnvelope
	if err := json.Unmarshal(line.Raw, &envelope); err != nil {
		return false
	}
	reqID := envelope.Response.RequestID
	a.pendingControlMu.Lock()
	ch, ok := a.pendingControl[reqID]
	a.pendingControlMu.Unlock()
	if !ok {
		return false
	}
	result := codebuddyControlResult{
		Success:     envelope.Response.Subtype == "success",
		Error:       envelope.Response.Error,
		RawResponse: envelope.Response.Response,
	}
	var inner struct {
		Mode string `json:"mode"`
	}
	if len(envelope.Response.Response) > 0 {
		_ = json.Unmarshal(envelope.Response.Response, &inner)
	}
	result.Mode = inner.Mode
	result.PermissionMode = inner.Mode
	select {
	case ch <- result:
	default:
	}
	return true
}

// sendControlFire sends a control request without waiting for its response.
func (a *Agent) sendControlFire(requestBody string) error {
	msg := `{"type":"control_request","request_id":"` + shortID() + `","request":` + requestBody + `}`
	return a.SendRawInput([]byte(msg))
}

// sendControlAndWait sends a control request and waits for its response.
func (a *Agent) sendControlAndWait(requestBody string, timeout time.Duration) (codebuddyControlResult, error) {
	requestID := shortID()
	ch := make(chan codebuddyControlResult, 1)
	a.pendingControlMu.Lock()
	a.pendingControl[requestID] = ch
	a.pendingControlMu.Unlock()
	defer func() {
		a.pendingControlMu.Lock()
		delete(a.pendingControl, requestID)
		a.pendingControlMu.Unlock()
	}()

	msg := `{"type":"control_request","request_id":"` + requestID + `","request":` + requestBody + `}`
	if err := a.SendRawInput([]byte(msg)); err != nil {
		select {
		case <-a.ProcessDone():
			return codebuddyControlResult{}, a.ProcessExitError()
		case <-time.After(1 * time.Second):
			return codebuddyControlResult{}, err
		}
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, errString(resp.Error)
		}
		return resp, nil
	case <-a.ProcessDone():
		return codebuddyControlResult{}, a.ProcessExitError()
	case <-time.After(timeout):
		return codebuddyControlResult{}, errControlTimeout
	}
}

// applyPermissionMode sends set_permission_mode and records the result.
func (a *Agent) applyPermissionMode(mode string) error {
	body, err := json.Marshal(map[string]string{
		"subtype": contracts.CodebuddyControlRequestSubtypeSetPermissionMode,
		"mode":    mode,
	})
	if err != nil {
		return err
	}
	resp, err := a.sendControlAndWait(string(body), 2*time.Second)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if resp.Mode != "" {
		a.permissionMode = resp.Mode
	} else {
		a.permissionMode = mode
	}
	a.mu.Unlock()
	return nil
}

// persistRaw forwards one verbatim NDJSON frame to the transcript.
func (a *Agent) persistRaw(raw []byte) {
	content := agent.MessageContent{Original: raw}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		slog.Debug("codebuddy: persist message", "agent_id", a.AgentID(), "error", err)
	}
}

// HandleOutput processes a single NDJSON line from CodeBuddy.
func (a *Agent) HandleOutput(content []byte) {
	a.handleOutput(&providerkit.ParsedLine{Raw: content, Type: string(envelopeType(content))})
}

// envelopeType extracts the top-level `type` of one frame.
func envelopeType(content []byte) MessageType {
	var envelope MessageEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return ""
	}
	return envelope.Type
}
