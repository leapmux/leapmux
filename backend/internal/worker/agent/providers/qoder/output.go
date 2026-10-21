package qoder

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// errControlTimeout reports a control request that got no answer in time.
var errControlTimeout = errors.New("the agent did not answer the control request in time")

// initializeTimeout limits the handshake, including native runtime startup.
// The limit permits slow startup on a loaded machine.
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
	if qoderDropsFrame(line.Type, providerkit.StringField(line.Raw, "subtype")) {
		return
	}
	switch line.Type {
	case contracts.QoderFrameKindSystem:
		a.handleSystem(line.Raw)
	case contracts.QoderFrameKindAssistant:
		a.handleAssistant(line.Raw)
	case contracts.QoderFrameKindResult:
		a.handleResult(line.Raw)
	case contracts.QoderFrameKindStreamEvent:
		a.handleRootStreamEvent(line.Raw)
	case frameTypeControlRequest:
		a.handleInboundControlRequest(line.Raw)
	case frameTypeControlCancelRequest:
		a.handleControlCancellation(line.Raw)
	case contracts.QoderFrameKindUser:
		a.handleWorkflowLaunch(line.Raw)
		a.handleWorkflowReplayCompletion(line.Raw)
		if qoderUserFrameHasToolResult(line.Raw) {
			if stored, _ := a.persistNativeToolFrame(a.sink, line.Raw); !stored {
				a.persistRaw(line.Raw)
			}
		}
	default:
		a.persistUnknownFrame(line.Raw)
	}
}

// handleControlCancellation withdraws a native request without a transcript row.
func (a *Agent) handleControlCancellation(raw []byte) {
	var frame struct {
		RequestID string `json:"request_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil || frame.RequestID == "" {
		slog.Debug("qoder ignored an invalid control cancellation", "agent_id", a.AgentID())
		return
	}
	if frame.SessionID != "" && frame.SessionID != a.nativeSessionID() {
		slog.Debug("qoder ignored a control cancellation for another session", "agent_id", a.AgentID())
		return
	}
	a.sink.CancelControlRequest(frame.RequestID)
}

// handleRootStreamEvent reports live generation. The complete assistant frame owns the stored text.
func (a *Agent) handleRootStreamEvent(raw []byte) {
	var frame qoderStreamEvent
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Event.Index < 0 || providerkit.RawPresent(frame.ParentToolUseID) {
		return
	}
	event := frame.Event
	var kind, text string
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock == nil {
			return
		}
		kind = event.ContentBlock.Type
		switch kind {
		case "text":
			text = event.ContentBlock.Text
		case "thinking":
			text = event.ContentBlock.Thinking
		default:
			return
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			kind, text = "text", event.Delta.Text
		case "thinking_delta":
			kind, text = "thinking", event.Delta.Thinking
		default:
			return
		}
		if text == "" {
			return
		}
	case "content_block_stop":
		a.mu.Lock()
		kind = a.rootStreamKinds[event.Index]
		delete(a.rootStreamKinds, event.Index)
		a.mu.Unlock()
		if kind != "" {
			a.sink.ReportProgress(agent.CompleteModelProgress(qoderProgressScope(kind, event.Index)))
		}
		return
	default:
		// The complete assistant and result frames carry the message boundaries and retry outcome.
		return
	}
	a.mu.Lock()
	if a.rootStreamKinds == nil {
		a.rootStreamKinds = make(map[int]string)
	}
	a.rootStreamKinds[event.Index] = kind
	a.mu.Unlock()
	if text != "" {
		a.notificationState("").retry.Resolve(a.sink)
		a.sink.ReportProgress(agent.ModelTextProgress(qoderProgressScope(kind, event.Index), text))
	}
}

func qoderProgressScope(kind string, index int) string {
	return "qoder:" + kind + ":" + strconv.Itoa(index)
}

// handleInboundControlRequest publishes a native request through the control service.
// The control service stores and displays its card. A separate transcript row would duplicate the request.
func (a *Agent) handleInboundControlRequest(raw []byte) {
	var envelope controlRequestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		slog.Warn("qoder: malformed control_request", "agent_id", a.AgentID(), "error", err)
		return
	}
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
	a.handleSystemTo(a.sink, raw, "")
}

func (a *Agent) handleSystemTo(target agent.ProviderServices, raw []byte, owner string) {
	root := owner == ""
	var envelope struct {
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		switch envelope.Subtype {
		case contracts.QoderSystemSubtypeInit:
			if !root {
				return
			}
			var init systemInitMessage
			if json.Unmarshal(raw, &init) == nil {
				a.mu.Lock()
				if init.SessionID != "" {
					a.sessionID = init.SessionID
				}
				// The SDK init frame carries a model display label, which the native reviewer cannot resolve.
				// Keep the native ID from launch or a confirmed model update.
				// The initialize catalog supplies the picker labels.
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
			return
		case contracts.QoderSystemSubtypeGoalUpdated:
			if root {
				a.handleGoalUpdated(raw)
			}
			return
		case contracts.QoderSystemSubtypeGoalCleared:
			if root {
				a.handleGoalCleared(raw)
			}
			return
		case contracts.QoderSystemSubtypeAvailableModelsUpdate:
			if root {
				a.handleAvailableModelsUpdate(raw)
			}
			return
		case contracts.QoderSystemSubtypePlanModeChanged:
			if root {
				a.handlePlanModeChanged(raw)
			}
			return
		case contracts.QoderSystemSubtypeInterrupt:
			return
		case qoderTaskStarted, qoderTaskNotification:
			if root && a.handleTaskEvent(raw) {
				return
			}
			slog.Debug("qoder ignored a task frame without an agent route", "agent_id", a.AgentID())
			return
		case contracts.QoderSystemSubtypeAgentsMdSizeWarning:
			a.mu.Lock()
			stored := a.storedInstructionWarning
			a.storedInstructionWarning = true
			a.mu.Unlock()
			if stored {
				return
			}
			a.persistNotificationTo(target, raw)
			return
		case contracts.QoderSystemSubtypeHookResponse:
			if providerkit.StringField(raw, "outcome") != "success" {
				a.persistNotificationTo(target, raw)
			}
			return
		case contracts.QoderSystemSubtypeStatus:
			var status struct {
				Status json.RawMessage `json:"status"`
			}
			if json.Unmarshal(raw, &status) == nil {
				if value, ok := providerkit.RawString(status.Status); ok && value == contracts.QoderStatusCompacting {
					state := a.notificationState(owner)
					a.mu.Lock()
					started := !state.compactionLive
					state.compactionLive = true
					a.mu.Unlock()
					if started {
						providerkit.PublishCompaction(target, "")
					}
					return
				}
				// A null status precedes the boundary. Keep the live key until that boundary or a result arrives.
				if providerkit.RawKindOf(status.Status) == providerkit.RawNull {
					return
				}
			}
		case contracts.QoderSystemSubtypeCompactBoundary:
			if !a.endNativeCompaction(target, owner) {
				providerkit.EndCompaction(target)
			}
			a.persistNotificationTo(target, raw)
			return
		case contracts.QoderSystemSubtypeApiRetry:
			a.persistNotificationTo(target, raw)
			a.notificationState(owner).retry.NoteRetry()
			return
		case contracts.QoderSystemSubtypeModelQueueStatus:
			a.persistNotificationTo(target, raw)
			return
		}
	}
	if root {
		a.persistUnknownFrame(raw)
	} else {
		a.persistRawTo(target, raw)
	}
}

type qoderNotificationState struct {
	compactionLive bool // Guarded by Agent.mu.
	retry          providerkit.RetryResolution
}

func (a *Agent) notificationState(owner string) *qoderNotificationState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.notifications == nil {
		a.notifications = make(map[string]*qoderNotificationState)
	}
	state := a.notifications[owner]
	if state == nil {
		state = &qoderNotificationState{}
		a.notifications[owner] = state
	}
	return state
}

// endNativeCompaction clears a live start. A native boundary remains valid when the start was absent.
func (a *Agent) endNativeCompaction(target agent.ProviderServices, owner string) bool {
	state := a.notificationState(owner)
	a.mu.Lock()
	live := state.compactionLive
	state.compactionLive = false
	a.mu.Unlock()
	if live {
		providerkit.EndCompaction(target)
	}
	return live
}

func (a *Agent) finishNotifications(target agent.ProviderServices, owner string, interrupted bool) {
	a.notificationState(owner).retry.Reset()
	if !a.endNativeCompaction(target, owner) {
		return
	}
	outcome := providerkit.CompactionEndFailed
	if interrupted {
		outcome = providerkit.CompactionEndAborted
	}
	providerkit.PersistCompactionEnded(target, outcome, "")
}

func qoderDropsFrame(frameType, subtype string) bool {
	switch frameType {
	case contracts.QoderFrameKindToolProgress, contracts.QoderFrameKindProgress, contracts.QoderFrameKindCommandLifecycle, contracts.QoderFrameKindAttachment:
		return true
	case contracts.QoderFrameKindSystem:
		switch subtype {
		case qoderSubtypeHookStarted, qoderSubtypeHookProgress, qoderSubtypePermissionDenied, qoderSubtypeElicitationComplete, qoderSubtypeArtifactsUpdate, qoderSubtypeCommandsChanged, qoderSubtypeControlRequestProgress, qoderSubtypeTaskProgress:
			return true
		}
	}
	return false
}

func qoderUserFrameHasToolResult(raw []byte) bool {
	var frame struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	for _, block := range frame.Message.Content {
		if block.Type == "tool_result" {
			return true
		}
	}
	return false
}

func (a *Agent) persistUnknownFrame(raw []byte) {
	sessionID := providerkit.StringField(raw, "session_id")
	if sessionID == "" || sessionID != a.nativeSessionID() {
		slog.Debug("qoder ignored an unknown frame without the current session", "agent_id", a.AgentID())
		return
	}
	a.persistRaw(raw)
}

func (a *Agent) persistNotificationTo(target agent.ProviderServices, raw []byte) {
	if _, err := target.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, qoderMessageContent(raw, a.nativeSessionID())); err != nil {
		slog.Error("qoder could not persist a notification", "agent_id", a.AgentID(), "error", err)
	}
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
	a.notificationState("").retry.Resolve(a.sink)
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
	a.mu.Lock()
	clear(a.rootStreamKinds)
	a.mu.Unlock()
	a.sink.ReportProgress(agent.ResetModelProgress())
	interrupted := a.takeInterruptRequest() && !qoderResultSucceeded(raw)
	a.finishNotifications(a.sink, "", interrupted)
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
	a.persistRawTo(a.sink, raw)
}

func (a *Agent) persistRawTo(target agent.ProviderServices, raw []byte) {
	content := qoderMessageContent(raw, a.nativeSessionID())
	if err := target.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		slog.Error("qoder: persist message", "agent_id", a.AgentID(), "error", err)
	}
}

// qoderMessageContent preserves the native flag on an aborted assistant frame.
// Qoder sends this frame before the result, including when it cuts a text block.
func qoderMessageContent(raw []byte, sessionID string) agent.MessageContent {
	if explicit := providerkit.StringField(raw, "session_id"); explicit != "" {
		sessionID = explicit
	}
	content := agent.MessageContent{Original: raw, AgentSessionID: sessionID}
	var frame struct {
		Type    string `json:"type"`
		Aborted bool   `json:"aborted"`
	}
	if json.Unmarshal(raw, &frame) == nil && frame.Type == contracts.QoderFrameKindAssistant && frame.Aborted {
		content.Completion = agent.MessageCompletionInterrupted
	}
	return content
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
	nativeContent := qoderMessageContent(raw, sessionID)
	for _, block := range frame.Message.Content {
		content := nativeContent
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

// HandleOutput processes one NDJSON line through the same handlers as the process reader.
// It delivers control replies before it dispatches other frames.
func (a *Agent) HandleOutput(content []byte) {
	var envelope MessageEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return
	}
	line := &providerkit.ParsedLine{Raw: content, Type: envelope.Type}
	if a.handlePendingControlResponse(line) {
		return
	}
	a.handleOutput(line)
}

// handlePendingControlResponse delivers pending replies and consumes replies with no waiting caller.
// An interrupt has no waiting caller. Its reply carries no session identity or transcript content.
func (a *Agent) handlePendingControlResponse(line *providerkit.ParsedLine) bool {
	if line.Type != frameTypeControlResponse {
		return false
	}
	var envelope controlResponseEnvelope
	if err := json.Unmarshal(line.Raw, &envelope); err != nil {
		return false
	}
	if !a.controls.Deliver(envelope.Response.RequestID, line.Raw) {
		slog.Debug("qoder received a control reply without a waiting caller", "agent_id", a.AgentID(), "request_id", envelope.Response.RequestID)
	}
	return true
}

// sendControlFire sends a control request without waiting for its response.
func (a *Agent) sendControlFire(requestBody string) error {
	msg := `{"type":"control_request","request_id":"` + shortID() + `","request":` + requestBody + `}`
	return a.SendRawInput([]byte(msg), agent.StopContext{})
}

const qoderSendFailureExitTag = "qoder-send-failure-exit"

// sendControlAndWait sends a control request and waits on the process clock.
func (a *Agent) sendControlAndWait(requestBody string, timeout time.Duration) (qoderControlResult, error) {
	requestID := shortID()
	ch, release, err := a.controls.Register(requestID)
	if err != nil {
		return qoderControlResult{}, err
	}
	defer release()
	msg := `{"type":"control_request","request_id":"` + requestID + `","request":` + requestBody + `}`
	if err := a.SendRawInput([]byte(msg), agent.StopContext{}); err != nil {
		release()
		// A failed stdin write can precede the process exit report.
		timer := a.Clock().NewTimer(time.Second, qoderSendFailureExitTag)
		defer timer.Stop(qoderSendFailureExitTag)
		select {
		case <-a.ProcessDone():
			return qoderControlResult{}, a.ProcessExitError()
		case <-timer.C:
			return qoderControlResult{}, err
		}
	}
	subtype := providerkit.StringField([]byte(requestBody), "subtype")
	raw, err := providerkit.AwaitReply(&a.Process, a.Context(), ch, "qoder control "+subtype, timeout)
	if errors.Is(err, providerkit.ErrAwaitTimeout) {
		return qoderControlResult{}, errControlTimeout
	}
	if err != nil {
		return qoderControlResult{}, err
	}
	var envelope controlResponseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return qoderControlResult{}, fmt.Errorf("decode the Qoder control reply: %w", err)
	}
	result := qoderControlResult{
		Success:     envelope.Response.Subtype == "success",
		Error:       envelope.Response.Error,
		RawResponse: envelope.Response.Response,
		Mode:        providerkit.StringField(envelope.Response.Response, "mode"),
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "Qoder refused the control request"
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

// initializeStream completes Qoder's stream-json initialize handshake.
//
// The CLI refuses user messages and cancels the session until initialize succeeds.
// Its error is "initialize_required: user message received before successful initialize".
// Startup completes this handshake before it accepts input.
func (a *Agent) initializeStream() error {
	body, err := json.Marshal(map[string]any{
		"subtype":                       contracts.QoderControlRequestSubtypeInitialize,
		"supportsAvailableModelsUpdate": true,
	})
	if err != nil {
		return err
	}
	result, err := a.sendControlAndWait(string(body), initializeTimeout)
	if err != nil {
		return err
	}
	if !providerkit.RawPresent(result.RawResponse) {
		return nil
	}
	var response qoderInitializeResponse
	if err := json.Unmarshal(result.RawResponse, &response); err != nil {
		return fmt.Errorf("decode the Qoder initialize reply: %w", err)
	}
	a.applyModelCatalog(response.Models, "", true)
	return nil
}
