package qoder

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// qoderControlRefusal is a control request that Qoder answered with an error.
// Qoder handled the request, unlike a request that timed out or a process that
// exited, so its state can differ from the state before the request.
type qoderControlRefusal struct {
	message string
}

func (e *qoderControlRefusal) Error() string { return e.message }

// initializeTimeout limits the handshake, including native runtime startup.
// The limit permits slow startup on a loaded machine.
const initializeTimeout = 30 * time.Second

// shortID generates a short correlation id for a control request.
func shortID() string { return id.Short() }

// readOutputLoop reads NDJSON frames from stdout until the process exits.
func (a *Agent) readOutputLoop(scanner *bufio.Scanner) {
	defer func() {
		if a.outputDone != nil {
			close(a.outputDone)
		}
	}()
	a.ReadOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
	a.finishStream()
}

// handleOutput dispatches one NDJSON frame.
func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.routeChildFrame(line.Raw) {
		return
	}
	if line.Type == contracts.QoderFrameKindCommandLifecycle {
		a.handleCommandLifecycle(line.Raw)
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
		a.observeInputEcho(line.Raw)
		a.handleWorkflowLaunch(line.Raw)
		a.handleWorkflowReplayCompletion(line.Raw)
		if qoderUserFrameHasToolResult(line.Raw) {
			if stored, _ := a.persistNativeToolFrame(a.sink, "", line.Raw); !stored {
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
	if a.answerDaemonAuthentication(raw) {
		return
	}
	var envelope controlRequestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		slog.Warn("qoder: malformed control_request", "agent_id", a.AgentID(), "error", err)
		return
	}
	control := agent.ControlRequest{RequestID: envelope.RequestID, Payload: raw}
	a.scopeInputControl(&control, envelope)
	if err := a.sink.PublishControlRequest(control); err != nil {
		slog.Error("qoder: publish control request", "agent_id", a.AgentID(), "request_id", envelope.RequestID, "error", err)
		response, marshalErr := json.Marshal(map[string]any{
			"type":     frameTypeControlResponse,
			"response": map[string]any{"subtype": "error", "request_id": envelope.RequestID, "error": providerkit.ControlPublicationFailure},
		})
		if marshalErr != nil {
			slog.Error("qoder: encode control failure", "agent_id", a.AgentID(), "error", marshalErr)
			return
		}
		if err := a.SendRawInput(agent.RawInput{Data: response}); err != nil {
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
		case contracts.QoderSystemSubtypePermissionPolicy:
			if root {
				a.handlePermissionPolicy(raw)
			}
			return
		case contracts.QoderSystemSubtypeHookDiagnostics:
			a.persistNotificationTo(target, raw)
			return
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
		case qoderTaskStarted, qoderTaskNotification, qoderSubtypeTaskUpdated, qoderSubtypeBackgroundTasksChanged:
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
		case qoderSubtypeHookStarted, qoderSubtypeHookProgress, qoderSubtypePermissionDenied,
			qoderSubtypeElicitationComplete, qoderSubtypeArtifactsUpdate, qoderSubtypeCommandsChanged,
			qoderSubtypeControlRequestProgress, qoderSubtypeTaskProgress, qoderSubtypeMCPServerUpdated:
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
	frame, blocks, parsed := parseNativeToolFrame(raw)
	var reply *inputReply
	var sessionID string
	if parsed {
		explicitSession, _ := providerkit.RawString(frame.SessionID)
		parentID, hasParentStr := providerkit.RawString(frame.ParentToolUseID)
		root := !providerkit.RawPresent(frame.ParentToolUseID) || (hasParentStr && parentID == "")
		a.mu.Lock()
		if explicitSession != "" && a.sessionID == "" {
			a.sessionID = explicitSession
		}
		sessionID = explicitSession
		if sessionID == "" {
			sessionID = a.sessionID
		}
		if current := a.inputReply; root && current.owns(explicitSession) && current.result == nil {
			reply = current
		}
		a.mu.Unlock()
	} else {
		sessionID = a.nativeSessionID()
	}
	a.markTurnActiveFromOutput()
	nativeContent := qoderMessageContentFrom(raw, sessionID, frame.Type, parsed && providerkit.RawBool(frame.Aborted))
	var stored bool
	var opened int
	if parsed {
		stored, opened = a.persistParsedNativeToolFrame(a.sink, "", raw, sessionID, frame.Type, nativeContent, blocks)
	}
	if !stored {
		if reply != nil {
			nativeContent.NativeTurnID = reply.receipt.TurnID
		}
		if retained := a.persistMessageTo(a.sink, nativeContent); reply != nil && !retained {
			a.mu.Lock()
			reply.incomplete = true
			a.mu.Unlock()
		}
	}
	// routeChildFrame takes every frame of a child agent before this handler,
	// so each call counted here belongs to the root turn.
	a.Mu.Lock()
	a.TurnToolUses += opened
	a.Mu.Unlock()
}

// handleResult records the result at the native boundary. A receipt waits for
// the matching command end before the Worker settles the turn.
func (a *Agent) handleResult(raw []byte) {
	var frame struct {
		SessionID string `json:"session_id"`
		IsError   *bool  `json:"is_error"`
		Origin    struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		frame.IsError = nil
	}
	if current := a.nativeSessionID(); current != "" && frame.SessionID != "" && frame.SessionID != current {
		return
	}
	// Qoder can use subtype "success" for a failed turn. The is_error flag
	// also preserves a successful result when an interrupt arrives too late.
	interrupted := a.takeInterruptRequest() && (frame.IsError == nil || *frame.IsError)
	result := inputResult{raw: raw, isError: frame.IsError, interrupted: interrupted}
	// A result triggered by a task notification or a peer cannot prove
	// the outcome of an ordinary user input.
	sessionID := frame.SessionID
	if frame.Origin.Kind != "" && frame.Origin.Kind != "human" {
		sessionID = ""
	}
	held, earlier := a.holdInputResult(sessionID, result)
	if held {
		return
	}
	a.finishUnprovedInput(earlier)
	a.persistInputResult(result, agent.NativeInputReceipt{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
	a.setTurnActive(false)
}

// persistInputResult writes the native result with the tool count, stop
// outcome, and post-turn quota refresh. A proved command can also attach its
// input identity and outcome.
func (a *Agent) persistInputResult(result inputResult, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) bool {
	retained := a.persistTurnEndResult(result, receipt, outcome)
	// A turn spends credits, so the account quota is read again.
	a.requestUsageRefresh()
	return retained
}

// persistTurnEndResult persists the native result without requesting a post-turn
// quota refresh, so exit teardown can flush a held result cleanly.
func (a *Agent) persistTurnEndResult(result inputResult, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) bool {
	a.mu.Lock()
	clear(a.rootStreamKinds)
	a.mu.Unlock()
	// Qoder's `result` carries no num_tool_uses field, so the Worker counts the
	// tool calls of the turn itself. The count restarts here for the next turn.
	a.Mu.Lock()
	toolUses := a.TurnToolUses
	a.TurnToolUses = 0
	a.Mu.Unlock()
	if a.sink == nil {
		return false
	}
	a.sink.ReportProgress(agent.ResetModelProgress())
	a.finishNotifications(a.sink, "", result.interrupted)
	if result.interrupted {
		a.closeOpenToolsForTarget(a.sink, "", agent.MessageCompletionInterrupted, false)
	}
	sessionID := receipt.SessionID
	if sessionID == "" {
		sessionID = a.nativeSessionID()
	}
	content := agent.WithToolUseCount(agent.MessageContent{
		Original: result.raw, AgentSessionID: sessionID, NativeTurnID: receipt.TurnID, InputTurnOutcome: outcome,
	}, toolUses)
	if result.interrupted {
		content.Completion = agent.MessageCompletionInterrupted
	}
	err := a.sink.PersistTurnEnd(content, agent.SpanInfo{})
	if err != nil {
		slog.Error("Persist Qoder turn end", "agent_id", a.AgentID(), "error", err)
	}
	if result.interrupted {
		providerkit.ResetSpansAndProgress(a.sink)
	}
	a.broadcastContextUsage(result.raw)
	return agent.OutputRetained(err)
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
	a.persistMessageTo(target, qoderMessageContent(raw, a.nativeSessionID()))
}

func (a *Agent) persistMessageTo(target agent.ProviderServices, content agent.MessageContent) bool {
	err := target.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	if err != nil {
		slog.Error("qoder: persist message", "agent_id", a.AgentID(), "error", err)
	}
	return agent.OutputRetained(err)
}

// qoderMessageContent preserves the native flag on an aborted assistant frame.
// Qoder sends this frame before the result, including when it cuts a text block.
func qoderMessageContent(raw []byte, sessionID string) agent.MessageContent {
	fields := providerkit.RawFields(raw)
	if explicit := providerkit.ObjectString(fields, "session_id"); explicit != "" {
		sessionID = explicit
	}
	return qoderMessageContentFrom(raw, sessionID, providerkit.ObjectString(fields, "type"), providerkit.RawBool(fields["aborted"]))
}

func qoderMessageContentFrom(raw []byte, sessionID, frameType string, aborted bool) agent.MessageContent {
	content := agent.MessageContent{Original: raw, AgentSessionID: sessionID}
	if frameType == contracts.QoderFrameKindAssistant && aborted {
		content.Completion = agent.MessageCompletionInterrupted
	}
	return content
}

func parseNativeToolFrame(raw []byte) (qoderNativeFrame, []qoderToolBlock, bool) {
	var frame qoderNativeFrame
	if json.Unmarshal(raw, &frame) != nil {
		return qoderNativeFrame{}, nil, false
	}
	var blocks []qoderToolBlock
	if bytes.HasPrefix(bytes.TrimSpace(frame.Message.Content), []byte("[")) {
		if json.Unmarshal(frame.Message.Content, &blocks) != nil {
			blocks = nil
		}
	}
	return frame, blocks, true
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
func (a *Agent) persistNativeToolFrame(target agent.ProviderServices, childID string, raw []byte) (stored bool, opened int) {
	frame, blocks, ok := parseNativeToolFrame(raw)
	if !ok {
		return false, 0
	}
	sessionID, _ := providerkit.RawString(frame.SessionID)
	if sessionID == "" {
		sessionID = a.nativeSessionID()
	}
	nativeContent := qoderMessageContentFrom(raw, sessionID, frame.Type, providerkit.RawBool(frame.Aborted))
	return a.persistParsedNativeToolFrame(target, childID, raw, sessionID, frame.Type, nativeContent, blocks)
}

func (a *Agent) persistParsedNativeToolFrame(target agent.ProviderServices, childID string, raw []byte, sessionID, frameType string, nativeContent agent.MessageContent, blocks []qoderToolBlock) (stored bool, opened int) {
	var clonedRaw []byte
	for _, block := range blocks {
		content := nativeContent
		if frameType == contracts.QoderFrameKindAssistant && block.Type == "tool_use" && block.ID != "" && block.Name != "" {
			stored = true
			opened++
			if target == a.sink && block.Name == "Agent" && qoderToolInputRunsInBackground(block.Input) {
				a.markChildTaskBackground(block.ID, true)
			}
			spawns := block.Name == "Agent" || block.Name == "Workflow"
			if clonedRaw == nil {
				clonedRaw = bytes.Clone(raw)
			}
			a.openToolEntry(childID, block.ID, block.Name, spawns, qoderOpenTool{
				raw:       clonedRaw,
				sessionID: sessionID,
			})
			if err := providerkit.OpenToolSpan(target, content, block.ID, block.Name, spawns); err != nil {
				slog.Error("Persist Qoder tool request", "agent_id", a.AgentID(), "tool_call_id", block.ID, "error", err)
			}
		} else if frameType == contracts.QoderFrameKindUser && block.Type == "tool_result" && block.CallID != "" {
			stored = true
			entry, wasOpen, alreadyClosed := a.completeNativeToolEntry(childID, block.CallID)
			if alreadyClosed {
				continue
			}
			name := target.GetSpanType(block.CallID)
			if name == "" && wasOpen {
				name = entry.SpanType
			}
			spawns := (wasOpen && entry.NoSpan) || name == "Agent" || name == "Workflow"
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

func (a *Agent) toolLedgerLocked(childID string, create bool) *providerkit.ToolSpanLedger[qoderOpenTool] {
	if childID == "" {
		return &a.rootTools
	}
	if a.childTools == nil {
		if !create {
			return nil
		}
		a.childTools = make(map[string]*providerkit.ToolSpanLedger[qoderOpenTool])
	}
	ledger := a.childTools[childID]
	if ledger == nil && create {
		ledger = &providerkit.ToolSpanLedger[qoderOpenTool]{}
		a.childTools[childID] = ledger
	}
	return ledger
}

func (a *Agent) openToolEntry(childID, spanID, spanType string, noSpan bool, data qoderOpenTool) {
	a.toolMu.Lock()
	a.toolLedgerLocked(childID, true).Open(spanID, spanType, noSpan, data)
	a.toolMu.Unlock()
}

func (a *Agent) completeNativeToolEntry(childID, spanID string) (providerkit.OpenToolEntry[qoderOpenTool], bool, bool) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	ledger := a.toolLedgerLocked(childID, false)
	if ledger == nil {
		return providerkit.OpenToolEntry[qoderOpenTool]{}, false, false
	}
	return ledger.CompleteNative(spanID)
}

// closeOpenToolsForTarget drains open tool entries for root (childID == "") or
// a child transcript and closes each with completion. When includeSpawns is
// false, Agent and Workflow spawning tool entries stay tracked so their native
// tool_result frames still resolve normally.
func (a *Agent) closeOpenToolsForTarget(target agent.ProviderServices, childID string, completion agent.MessageCompletion, includeSpawns bool) {
	a.toolMu.Lock()
	ledger := a.toolLedgerLocked(childID, false)
	var entries []providerkit.OpenToolEntry[qoderOpenTool]
	if ledger != nil {
		if includeSpawns {
			entries = ledger.DrainAll()
		} else {
			entries = ledger.DrainMatching(func(entry providerkit.OpenToolEntry[qoderOpenTool]) bool {
				return !entry.NoSpan
			})
		}
	}
	a.toolMu.Unlock()
	if len(entries) == 0 || target == nil {
		return
	}
	discarding := a.IsDiscardingOutput()
	providerkit.CloseToolSpans(target, entries, completion, func(entry providerkit.OpenToolEntry[qoderOpenTool]) (agent.MessageContent, agent.SpanInfo, bool) {
		if discarding || len(entry.Data.raw) == 0 {
			return agent.MessageContent{}, agent.SpanInfo{}, false
		}
		content := agent.MessageContent{
			Original:       entry.Data.raw,
			AgentSessionID: entry.Data.sessionID,
			Completion:     completion,
		}
		return content, agent.SpanInfo{SpanID: entry.SpanID, SpanType: entry.SpanType, Closing: true, NoSpan: entry.NoSpan}, true
	})
}

// finishStream flushes any held input result, closes remaining open root and
// child tool spans, closes open task and workflow background rows, resets spans
// and progress, and retires queued steers on process exit.
func (a *Agent) finishStream() {
	a.finishOnce.Do(func() {
		a.outputMu.Lock()
		defer a.outputMu.Unlock()
		a.mu.Lock()
		var unfinished *inputReply
		unfinished, a.inputReply = a.inputReply, nil
		a.mu.Unlock()
		a.finishUnprovedInputOnExit(unfinished)

		completion := a.ProcessExitCompletion()
		taskStatus := agent.IncompleteTaskStatus(completion)
		a.closeOpenToolsForTarget(a.sink, "", completion, true)

		a.toolMu.Lock()
		childIDs := make([]string, 0, len(a.childTools))
		for childID := range a.childTools {
			childIDs = append(childIDs, childID)
		}
		a.toolMu.Unlock()
		slices.Sort(childIDs)
		for _, childID := range childIDs {
			var childSink agent.ProviderServices
			if a.sink != nil {
				childSink = a.sink.ChildSink(childID)
			}
			a.closeOpenToolsForTarget(childSink, childID, completion, true)
			providerkit.ResetSpansAndProgress(childSink)
		}
		a.toolMu.Lock()
		clear(a.childTools)
		a.toolMu.Unlock()

		a.mu.Lock()
		taskKeys := make([]string, 0, len(a.childTaskIDs))
		for key := range a.childTaskIDs {
			taskKeys = append(taskKeys, key)
		}
		clear(a.childTaskIDs)
		workflows := make([]*qoderWorkflowRun, 0, len(a.workflows))
		for _, run := range a.workflows {
			workflows = append(workflows, run)
		}
		clear(a.workflows)
		a.mu.Unlock()
		slices.SortFunc(workflows, func(left, right *qoderWorkflowRun) int {
			return cmp.Or(cmp.Compare(left.key.sessionID, right.key.sessionID), cmp.Compare(left.key.toolUseID, right.key.toolUseID))
		})

		if a.sink != nil {
			slices.Sort(taskKeys)
			for _, key := range taskKeys {
				if childID, _, found, err := a.sink.LookupBackgroundTask(key); err == nil && found && childID != "" {
					providerkit.ResetSpansAndProgress(a.sink.ChildSink(childID))
					providerkit.LogRegistryRefusal("qoder", "close child on exit", a.sink.CloseBackgroundTask(key, taskStatus))
					a.sink.CleanupChildAgent(childID)
				} else {
					providerkit.LogRegistryRefusal("qoder", "close child on exit", a.sink.CloseBackgroundTask(key, taskStatus))
				}
			}
			for _, run := range workflows {
				for _, taskID := range run.order {
					child := run.children[taskID]
					providerkit.LogRegistryRefusal("qoder", "close workflow child on exit", a.sink.CloseBackgroundTask(taskID, taskStatus))
					if child != nil && child.childID != "" {
						providerkit.ResetSpansAndProgress(a.sink.ChildSink(child.childID))
						a.sink.CleanupChildAgent(child.childID)
					}
				}
				providerkit.LogRegistryRefusal("qoder", "close workflow on exit", a.sink.CloseBackgroundTask(qoderWorkflowRowKey(run.key), taskStatus))
			}
			providerkit.ResetSpansAndProgress(a.sink)
		}
		a.retireQueuedSteers()
		a.setTurnActive(false)
	})
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
	return a.SendRawInput(agent.RawInput{Data: []byte(msg)})
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
	if err := a.SendRawInput(agent.RawInput{Data: []byte(msg)}); err != nil {
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
		SessionID:   envelope.SessionID,
		Error:       envelope.Response.Error,
		RawResponse: envelope.Response.Response,
		Mode:        providerkit.StringField(envelope.Response.Response, "mode"),
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "Qoder refused the control request"
		}
		return result, &qoderControlRefusal{message: result.Error}
	}
	return result, nil
}

// initializeStream initializes the SDK session before it accepts input.
// Catalog readiness makes Qoder apply the selected model after account discovery.
func (a *Agent) initializeStream() error {
	request := map[string]any{
		"subtype":                        contracts.QoderControlRequestSubtypeInitialize,
		"supportsAvailableModelsUpdate":  true,
		"supportsCatalogReadyInitialize": true,
		"includePartialMessages":         true,
	}
	if model := a.opts.Model(); model != "" {
		request["model"] = model
	}
	body, err := json.Marshal(request)
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
	running, styles, malformed := qoderOutputStyleReport(response)
	if malformed {
		slog.Warn("qoder: unreadable output styles in the initialize reply", "agent_id", a.AgentID())
	}
	a.applyOutputStyles(running, styles)
	return nil
}
