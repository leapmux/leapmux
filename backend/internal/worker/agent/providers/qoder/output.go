package qoder

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// errControlTimeout reports a control request that got no answer in time.
var errControlTimeout = errors.New("timeout waiting for agent to respond")

// initializeTimeout is the handshake deadline. Qoder boots its runtime before
// it answers initialize; the deadline is generous so a loaded machine does not
// fail an agent that is merely slow.
const initializeTimeout = 30 * time.Second

// shortID generates a short correlation id for a control request.
func shortID() string { return id.Short() }

// readOutputLoop reads NDJSON frames from stdout until the process exits.
func (a *Agent) readOutputLoop(scanner *bufio.Scanner) {
	a.ReadOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
}

// handleOutput dispatches one NDJSON frame.
func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	if a.routeChildFrame(line.Raw) {
		return
	}
	switch line.Type {
	case contracts.QoderFrameKindSystem:
		a.handleSystem(line.Raw)
	case contracts.QoderFrameKindAssistant:
		a.handleAssistant(line.Raw)
	case contracts.QoderFrameKindResult:
		a.handleResult(line.Raw)
	case frameTypeControlRequest:
		a.handleInboundControlRequest(line.Raw)
	case "user":
		a.handleWorkflowLaunch(line.Raw)
		if stored, _ := a.persistNativeToolFrame(a.sink, line.Raw); !stored {
			a.persistRaw(line.Raw)
		}
		a.handleWorkflowReplayCompletion(line.Raw)
	default:
		// stream_event, command_lifecycle, tool_progress, progress,
		// attachment: forward verbatim for the browser plugin.
		a.persistRaw(line.Raw)
	}
}

// handleInboundControlRequest publishes a can_use_tool control_request so the
// worker can raise a control banner and answer it. The publish is what raises
// the banner: the transcript copy alone never reaches the hub's control-request
// store, and the CLI then waits on a request nobody can answer until stop
// cancels it.
func (a *Agent) handleInboundControlRequest(raw []byte) {
	var envelope controlRequestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		slog.Warn("qoder: malformed control_request", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.persistRaw(raw)
	if err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: envelope.RequestID, Payload: raw}); err != nil {
		slog.Error("qoder: publish control request", "agent_id", a.AgentID(), "request_id", envelope.RequestID, "error", err)
		response, marshalErr := json.Marshal(map[string]any{
			"type":     frameTypeControlResponse,
			"response": map[string]any{"subtype": "error", "request_id": envelope.RequestID, "error": providerkit.ControlPublicationFailure},
		})
		if marshalErr != nil {
			slog.Error("qoder: encode control failure", "agent_id", a.AgentID(), "error", marshalErr)
			return
		}
		if err := a.SendRawInput(response, agent.StopContext{}); err != nil {
			slog.Warn("qoder: send control failure", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// handleSystem applies native state, consumes task events, and publishes other frames.
func (a *Agent) handleSystem(raw []byte) {
	var envelope struct {
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		switch envelope.Subtype {
		case contracts.QoderSystemSubtypeInit:
			var init systemInitMessage
			if json.Unmarshal(raw, &init) == nil {
				a.mu.Lock()
				if init.SessionID != "" {
					a.sessionID = init.SessionID
				}
				if init.Model != "" {
					a.model = init.Model
				}
				if init.Permission != "" {
					a.permissionMode = init.Permission
				}
				a.capabilities = init.Capabilities
				sessionID := a.sessionID
				a.mu.Unlock()
				if sessionID != "" {
					a.sink.UpdateSessionID(sessionID)
				}
			}
		case contracts.QoderSystemSubtypeGoalUpdated:
			a.handleGoalUpdated(raw)
		case contracts.QoderSystemSubtypeGoalCleared:
			a.handleGoalCleared(raw)
		case contracts.QoderSystemSubtypeAvailableModelsUpdate:
			a.handleAvailableModelsUpdate(raw)
		case contracts.QoderSystemSubtypePlanModeChanged:
			a.handlePlanModeChanged(raw)
		case qoderTaskStarted, qoderTaskNotification:
			if a.handleTaskEvent(raw) {
				return
			}
		}
	}
	a.persistRaw(raw)
}

// handlePlanModeChanged applies Qoder's working state without losing its policy.
func (a *Agent) handlePlanModeChanged(raw []byte) {
	var frame struct {
		PlanMode struct {
			Active *bool `json:"active"`
		} `json:"plan_mode"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.PlanMode.Active == nil {
		return
	}
	a.mu.Lock()
	a.planMode = *frame.PlanMode.Active
	if !a.planMode && a.permissionMode == contracts.QoderModePlan {
		a.permissionMode = contracts.QoderModeDefault
	}
	a.mu.Unlock()
}

// handleAssistant retains the native frame and marks its turn active.
func (a *Agent) handleAssistant(raw []byte) {
	var msg assistantMessage
	if json.Unmarshal(raw, &msg) == nil {
		a.mu.Lock()
		if msg.SessionID != "" && a.sessionID == "" {
			a.sessionID = msg.SessionID
		}
		a.mu.Unlock()
	}
	a.markTurnActiveFromOutput()
	stored, opened := a.persistNativeToolFrame(a.sink, raw)
	if !stored {
		a.persistRaw(raw)
	}
	// routeChildFrame takes every frame of a child agent before this handler,
	// so each call counted here belongs to the root turn.
	a.Mu.Lock()
	a.TurnToolUses += opened
	a.Mu.Unlock()
}

// handleResult ends the turn and publishes the frame verbatim, with the
// turn's tool count as worker metadata and the user's stop as its completion.
func (a *Agent) handleResult(raw []byte) {
	interrupted := a.takeInterruptRequest() && !qoderResultSucceeded(raw)
	// Qoder's `result` carries no num_tool_uses field, so the Worker counts the
	// tool calls of the turn itself. The count restarts here for the next turn.
	a.Mu.Lock()
	toolUses := a.TurnToolUses
	a.TurnToolUses = 0
	a.Mu.Unlock()
	content := agent.WithToolUseCount(agent.MessageContent{Original: raw, AgentSessionID: a.nativeSessionID()}, toolUses)
	if interrupted {
		content.Completion = agent.MessageCompletionInterrupted
	}
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Error("Persist Qoder turn end", "agent_id", a.AgentID(), "error", err)
	}
	a.setTurnActive(false)
	a.broadcastContextUsage(raw)
}

// qoderResultSucceeded reports whether a `result` states a turn that finished.
// A turn that finished before Qoder read an interrupt keeps its own outcome,
// because the stop came too late to change it.
//
// The `is_error` flag decides, not the subtype: qodercli 1.1.65 also writes
// `subtype: "success"` with `is_error: true` for a failed turn. A `result`
// that does not state the flag does not state success.
func qoderResultSucceeded(raw []byte) bool {
	var result struct {
		IsError *bool `json:"is_error"`
	}
	return json.Unmarshal(raw, &result) == nil && result.IsError != nil && !*result.IsError
}

// markTurnActiveFromOutput starts the turn when a frame arrives while idle.
func (a *Agent) markTurnActiveFromOutput() {
	a.mu.Lock()
	wasActive := a.active
	a.mu.Unlock()
	if !wasActive {
		a.setTurnActive(true)
	}
}

// persistRaw forwards one verbatim NDJSON frame to the transcript.
func (a *Agent) persistRaw(raw []byte) {
	content := agent.MessageContent{Original: raw, AgentSessionID: a.nativeSessionID()}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		slog.Debug("qoder: persist message", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) nativeSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

// persistNativeToolFrame stores the tool calls and the tool results of one
// frame. Each tool keeps its native ID and the unchanged frame that declares
// it. The function reports whether it stored the frame, and how many tool calls
// the frame opened.
func (a *Agent) persistNativeToolFrame(target agent.ProviderServices, raw []byte) (stored bool, opened int) {
	var frame struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Message   struct {
			Content []struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Name    string `json:"name"`
				CallID  string `json:"tool_use_id"`
				IsError bool   `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return false, 0
	}
	sessionID := frame.SessionID
	if sessionID == "" {
		sessionID = a.nativeSessionID()
	}
	for _, block := range frame.Message.Content {
		content := agent.MessageContent{Original: raw, AgentSessionID: sessionID}
		if frame.Type == contracts.QoderFrameKindAssistant && block.Type == "tool_use" && block.ID != "" && block.Name != "" {
			stored = true
			opened++
			spawns := block.Name == "Agent" || block.Name == "Workflow"
			if err := providerkit.OpenToolSpan(target, content, block.ID, block.Name, spawns); err != nil {
				slog.Error("Persist Qoder tool request", "agent_id", a.AgentID(), "tool_call_id", block.ID, "error", err)
			}
		} else if frame.Type == contracts.QoderFrameKindUser && block.Type == "tool_result" && block.CallID != "" {
			stored = true
			name := target.GetSpanType(block.CallID)
			spawns := name == "Agent" || name == "Workflow"
			if block.IsError {
				content.Completion = agent.MessageCompletionError
			}
			if err := target.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
				SpanID: block.CallID, SpanType: name, Closing: true, NoSpan: spawns,
			}); err != nil {
				slog.Error("Persist Qoder tool result", "agent_id", a.AgentID(), "tool_call_id", block.CallID, "error", err)
			}
			if !spawns {
				target.CloseSpan(block.CallID)
			}
		}
	}
	return stored, opened
}

// HandleOutput processes a single NDJSON line from Qoder. It runs the same
// pipeline as the reader loop: a pending control_response is answered first,
// and only an unconsumed line reaches handleOutput.
func (a *Agent) HandleOutput(content []byte) {
	var envelope MessageEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return
	}
	line := &providerkit.ParsedLine{Raw: content, Type: string(envelope.Type)}
	if a.handlePendingControlResponse(line) {
		return
	}
	a.handleOutput(line)
}

// handlePendingControlResponse intercepts a control_response that answers one
// of this agent's own pending requests.
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
	result := qoderControlResult{
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
	select {
	case ch <- result:
	default:
	}
	return true
}

// sendControlFire sends a control request without waiting for its response.
func (a *Agent) sendControlFire(requestBody string) error {
	msg := `{"type":"control_request","request_id":"` + shortID() + `","request":` + requestBody + `}`
	return a.SendRawInput([]byte(msg), agent.StopContext{})
}

// sendControlAndWait sends a control request and waits for its response.
func (a *Agent) sendControlAndWait(requestBody string, timeout time.Duration) (qoderControlResult, error) {
	requestID := shortID()
	ch := make(chan qoderControlResult, 1)
	a.pendingControlMu.Lock()
	a.pendingControl[requestID] = ch
	a.pendingControlMu.Unlock()
	defer func() {
		a.pendingControlMu.Lock()
		delete(a.pendingControl, requestID)
		a.pendingControlMu.Unlock()
	}()

	msg := `{"type":"control_request","request_id":"` + requestID + `","request":` + requestBody + `}`
	if err := a.SendRawInput([]byte(msg), agent.StopContext{}); err != nil {
		select {
		case <-a.ProcessDone():
			return qoderControlResult{}, a.ProcessExitError()
		case <-time.After(1 * time.Second):
			return qoderControlResult{}, err
		}
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	case <-a.ProcessDone():
		return qoderControlResult{}, a.ProcessExitError()
	case <-time.After(timeout):
		return qoderControlResult{}, errControlTimeout
	}
}

// initializeStream completes Qoder's stream-json initialize handshake.
//
// The CLI refuses every user message until one initialize succeeds, with
// `initialize_required: user message received before successful initialize`
// and a cancelled session, so the launch runs this before it accepts input.
func (a *Agent) initializeStream() error {
	body, err := json.Marshal(map[string]string{
		"subtype": contracts.QoderControlRequestSubtypeInitialize,
	})
	if err != nil {
		return err
	}
	_, err = a.sendControlAndWait(string(body), initializeTimeout)
	return err
}
