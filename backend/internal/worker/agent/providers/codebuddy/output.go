package codebuddy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	if a.consumeResumeFrame(line) {
		return
	}
	if a.routeChildFrame(line.Type, line.Raw) {
		return
	}
	switch line.Type {
	case frameTypeStreamEvent:
		a.handleStreamEvent(line.Raw)
	case frameTypeFileHistorySnapshot, frameTypeAiTitle, frameTypeTopic, contracts.CodebuddyFrameKindControlNotification:
		// These frames contain native history metadata or control receipts. They supply no chat row.
		return
	case contracts.CodebuddyFrameKindSystem:
		a.handleSystem(line.Raw)
	case contracts.CodebuddyFrameKindAssistant:
		a.handleAssistant(line.Raw)
	case contracts.CodebuddyFrameKindResult:
		a.handleResult(line.Raw)
	case contracts.CodebuddyFrameKindUser:
		a.observeAgentToolResults(line.Raw)
		rejectedPlan := a.observePlanModeToolResults(line.Raw)
		a.persistRaw(line.Raw)
		if rejectedPlan {
			a.mu.Lock()
			a.pendingPlanModeTools = nil
			a.mu.Unlock()
			// The rejection row ends the turn, so the turn's verdict follows it.
			providerkit.ApplyTurnFailure(a.sink, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}, line.Raw)
			a.setTurnActive(false)
		}
	case frameTypeControlRequest:
		a.handleInboundControlRequest(line.Raw)
	case frameTypeControlResponse:
		a.handleUnclaimedControlResponse(line.Raw)
	case contracts.CodebuddyFrameKindConversationReset:
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: line.Raw}); err != nil {
			slog.Debug("codebuddy: persist notification", "agent_id", a.AgentID(), "error", err)
		}
	default:
		// Preserve an unknown frame for inspection.
		a.persistUnknown(line.Raw)
	}
}

// handleUnclaimedControlResponse consumes receipts for requests that need no synchronous reply.
// A failed receipt still supplies a user-visible error. Unknown response shapes remain inspectable.
func (a *Agent) handleUnclaimedControlResponse(raw []byte) {
	var envelope controlResponseEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Response.RequestID == "" {
		a.persistUnknown(raw)
		return
	}
	switch envelope.Response.Subtype {
	case "success":
		return
	case "error":
		detail := strings.TrimSpace(envelope.Response.Error)
		if detail == "" {
			detail = "CodeBuddy refused a control request."
		}
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: detail,
		})
	default:
		a.persistUnknown(raw)
	}
}

// handleSystem reads init and task frames, then publishes other frames verbatim.
func (a *Agent) handleSystem(raw []byte) {
	var envelope struct {
		Subtype string          `json:"subtype"`
		Status  json.RawMessage `json:"status"`
		Event   string          `json:"event"`
	}
	if a.handleTaskEvent(raw) {
		return
	}
	if json.Unmarshal(raw, &envelope) != nil {
		a.persistUnknown(raw)
		return
	}
	if envelope.Subtype == contracts.CodebuddySystemSubtypeInit {
		var init systemInitMessage
		if json.Unmarshal(raw, &init) == nil {
			mode := init.PermissionMode
			a.mu.Lock()
			// Native child init frames have their own session ID but no parent tag.
			// Start pins this process's root session before any output arrives.
			if init.SessionID == "" || init.SessionID != a.sessionID {
				a.mu.Unlock()
				return
			}
			if init.Model != "" {
				a.model = init.Model
			}
			if mode != "" {
				a.permissionMode = mode
			}
			sessionID := a.sessionID
			a.mu.Unlock()
			if sessionID != "" {
				a.sink.UpdateSessionID(sessionID)
			}
			// A new init can follow a rejected plan exit. Its mode is native state,
			// even when the previous turn already reported the same mode.
			if mode != "" {
				a.sink.UpdatePermissionMode(mode)
				a.queuePermissionApprovals()
			}
		}
		return
	}
	switch envelope.Subtype {
	case contracts.CodebuddySystemSubtypeKeepalive:
		return
	case contracts.CodebuddySystemSubtypeStatus:
		a.publishCompactionStatus(envelope.Status)
		return
	case contracts.CodebuddySystemSubtypeMcpStatus:
		if envelope.Event != mcpStatusFinishEvent {
			return
		}
	case contracts.CodebuddySystemSubtypeInformational:
		// The notice supplies its own display text.
	default:
		a.persistUnknown(raw)
		return
	}
	a.persistRaw(raw)
}

// publishCompactionStatus reads an explicit start or end. An absent status changes nothing.
func (a *Agent) publishCompactionStatus(raw json.RawMessage) {
	status, _ := providerkit.RawString(raw)
	a.mu.Lock()
	started := status == "compacting" && !a.compactionLive
	// Manual commands can clear previous progress before their summary starts. Their result owns the end.
	ended := providerkit.RawKindOf(raw) == providerkit.RawNull && a.compactionLive && !a.manualCompaction
	if started {
		a.compactionLive = true
	} else if ended {
		a.compactionLive = false
	}
	a.mu.Unlock()
	if started {
		providerkit.PublishCompaction(a.sink, "")
	} else if ended {
		providerkit.PersistCompactionEnded(a.sink, providerkit.CompactionEndCompleted, "")
		providerkit.EndCompaction(a.sink)
	}
}

// handleAssistant publishes the frame verbatim and arms the turn from output.
// A compaction owns the turn it runs in: its summary is live state that draws
// no answer row (decision 21), so its assistant frames reach no transcript.
func (a *Agent) handleAssistant(raw []byte) {
	a.armTurnFromOutput()
	a.mu.Lock()
	compacting := a.compactionLive
	if compacting {
		// The compaction's own result must not backfill this text as the
		// turn's answer: the stream did carry it, and the summary draws no row.
		a.assistantTextStreamed = true
	}
	a.mu.Unlock()
	if compacting {
		return
	}
	var msg assistantMessage
	if json.Unmarshal(raw, &msg) == nil {
		a.completeModelStream("", raw)
		a.mu.Lock()
		a.lastDiagnostic = msg.diagnostic()
		if msg.SessionID != "" && a.sessionID == "" {
			a.sessionID = msg.SessionID
		}
		for _, block := range msg.Message.Content {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				a.assistantTextStreamed = true
			}
			if block.Type == "tool_use" && block.ID != "" {
				if strings.TrimSpace(block.ID) != "" && strings.TrimSpace(block.Name) != "" {
					if a.turnToolCalls == nil {
						a.turnToolCalls = make(map[string]struct{})
					}
					a.turnToolCalls[block.ID] = struct{}{}
				}
				kind := (codebuddyProvider{}).PlanModeControl(block.Name)
				if kind == agent.PlanModeControlEnter || kind == agent.PlanModeControlExit {
					if a.pendingPlanModeTools == nil {
						a.pendingPlanModeTools = make(map[string]agent.PlanModeControlKind)
					}
					a.pendingPlanModeTools[block.ID] = kind
				}
			}
		}
		a.mu.Unlock()
	}
	a.observeAgentToolUses(raw)
	a.persistRaw(raw)
}

// handleResult ends the turn and publishes the frame verbatim. A `result` ends
// a turn and NOT the process: CodeBuddy may still push background
// task_notification frames after it, so the reader keeps going.
func (a *Agent) handleResult(raw []byte) {
	var result resultMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		slog.Warn("codebuddy: malformed result", "agent_id", a.AgentID(), "error", err)
	} else {
		a.backfillMissingAssistantText(result)
		a.mu.Lock()
		model := a.model
		a.mu.Unlock()
		if usage := codebuddyContextUsage(result, model); usage != nil {
			a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
		}
	}
	a.mu.Lock()
	diagnostic := a.lastDiagnostic
	a.lastDiagnostic = nil
	a.assistantTextStreamed = false
	a.pendingPlanModeTools = nil
	toolUses := len(a.turnToolCalls)
	a.turnToolCalls = nil
	// One `result` ends one turn, so it spends the notes of every stop request
	// whatever it states. The notes mark the turn end only when the frame also states
	// that a stop took effect: a turn that ended before the CLI read the stop keeps
	// its outcome.
	stopRequested := len(a.interruptRequests) > 0
	a.interruptRequests = nil
	permissionStop := a.permissionInterruptPending
	a.permissionInterruptPending = false
	sessionID := a.sessionID
	interrupted := stopRequested && (result.statesAbortedTurn() || permissionStop && codebuddyPermissionInterruptedResult(result))
	a.mu.Unlock()
	// The SDK refusal must finish the main run before the global stop cancels
	// child tasks and workflows. Send that stop before the input queue resumes.
	if permissionStop {
		if err := a.sendInterruptControl(sessionID); err != nil {
			slog.Error("codebuddy: stop child tasks after the permission interrupt", "agent_id", a.AgentID(), "error", err)
		}
	}
	content := agent.WithToolUseCount(agent.MessageContent{Original: raw}, toolUses)
	requestID, _ := providerkit.RawString(result.RequestID)
	if diagnostic != nil && result.IsError && len(result.Errors) > 0 &&
		requestID == diagnostic.requestID && result.SessionID == diagnostic.sessionID && result.Errors[0] == diagnostic.text {
		supplement, err := json.Marshal(contracts.CodebuddyResultSupplement{
			RequestID: diagnostic.requestID, SessionID: diagnostic.sessionID, RepeatedError: diagnostic.text,
		})
		if err != nil {
			slog.Error("encode the CodeBuddy result supplement", "agent_id", a.AgentID(), "error", err)
		} else {
			content.Supplemental = supplement
		}
	}
	if interrupted {
		content.Completion = agent.MessageCompletionInterrupted
	}
	// A manual /compact ends at its result. The compacted summary never drew a
	// row, so the boundary row is the one record the compaction leaves.
	a.mu.Lock()
	manualEnded := a.manualCompaction
	a.mu.Unlock()
	if manualEnded {
		outcome := providerkit.CompactionEndCompleted
		switch {
		case interrupted:
			outcome = providerkit.CompactionEndAborted
		case result.IsError:
			outcome = providerkit.CompactionEndFailed
		}
		providerkit.PersistCompactionEnded(a.sink, outcome, "")
	}
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Warn("codebuddy: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
	// A turn that the user stopped never retries, even when its own failure won
	// the race against the stop. A manual /compact never retries either, whatever
	// its result states.
	failure := providerkit.CompactionEnd()
	if !manualEnded {
		failure = codebuddyResultFailure(result, stopRequested || permissionStop, a.Clock().Now())
	}
	providerkit.ApplyTurnFailure(a.sink, failure, raw)
	a.sink.ResetSpans()
	a.setTurnActive(false)
	a.mu.Lock()
	refreshStyles := a.outputStyles != nil
	a.mu.Unlock()
	if refreshStyles && !a.IsStopped() {
		a.catalogRefresh.Run("output-styles", a.refreshOutputStyleCatalog)
	}
}

// codebuddyDiagnostic records a displayed runtime message until its request ends.
// It supplies a repeat fact only after the native result confirms the same error.
type codebuddyDiagnostic struct {
	requestID, sessionID, text string
}

func (message assistantMessage) diagnostic() *codebuddyDiagnostic {
	requestID, _ := providerkit.RawString(message.RequestID)
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(message.SessionID) == "" || providerkit.RawPresent(message.MessageID) ||
		message.ParentToolUseID != nil && *message.ParentToolUseID != "" ||
		len(message.Message.Content) != 1 {
		return nil
	}
	var usage struct {
		OutputTokens json.RawMessage `json:"output_tokens"`
	}
	if json.Unmarshal(message.Message.Usage, &usage) != nil {
		return nil
	}
	output, ok := providerkit.RawInt64(usage.OutputTokens)
	if !ok || output != 0 {
		return nil
	}
	block := message.Message.Content[0]
	if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
		return nil
	}
	return &codebuddyDiagnostic{requestID: requestID, sessionID: message.SessionID, text: block.Text}
}

// The native plan tool results state when a plan starts or waits for revision.
// Stream JSON sends no turn end after a rejected ExitPlanMode. The rejection
// result ends that turn; a session-preserving restart restores native input.
const codebuddyPlanRejectionPrefix = "The user doesn't want to proceed with this plan yet."

func (a *Agent) observePlanModeToolResults(raw []byte) bool {
	var frame struct {
		SessionID string `json:"session_id"`
		Message   struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				IsError   *bool  `json:"is_error"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	a.mu.Lock()
	owned := frame.SessionID == "" || frame.SessionID == a.sessionID
	a.mu.Unlock()
	if !owned {
		return false
	}
	turnRejected := false
	for _, block := range frame.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		planRejected := false
		for _, part := range block.Content {
			if part.Type == "text" && strings.HasPrefix(strings.TrimSpace(part.Text), codebuddyPlanRejectionPrefix) {
				planRejected = true
				break
			}
		}
		a.mu.Lock()
		kind, pending := a.pendingPlanModeTools[block.ToolUseID]
		delete(a.pendingPlanModeTools, block.ToolUseID)
		enteredPlan := pending && kind == agent.PlanModeControlEnter && block.IsError != nil && !*block.IsError
		exitedPlan := pending && kind == agent.PlanModeControlExit && block.IsError != nil && !*block.IsError
		rejectedPlan := pending && kind == agent.PlanModeControlExit && block.IsError != nil && *block.IsError && planRejected
		confirmedMode := ""
		if enteredPlan {
			if a.permissionMode != contracts.CodebuddyModePlan {
				a.planReturnMode = a.permissionMode
			}
			confirmedMode = contracts.CodebuddyModePlan
		} else if exitedPlan {
			confirmedMode = a.permissionMode
			if confirmedMode == contracts.CodebuddyModePlan {
				// EnterPlanMode saves the prior mode in native process metadata.
				// Launching in Plan or selecting it through the SDK can leave it absent.
				confirmedMode = a.planReturnMode
				if confirmedMode == "" {
					confirmedMode = contracts.CodebuddyModeDefault
				}
			}
			a.planReturnMode = ""
		} else if rejectedPlan {
			confirmedMode = contracts.CodebuddyModePlan
		}
		turnRejected = turnRejected || rejectedPlan
		if confirmedMode != "" {
			a.permissionMode = confirmedMode
			// The native print loop records a rejected plan with skipRun and can
			// leave later prompts queued. Even matching launch settings need a
			// new process to restore input for this session.
			a.nativeModeRestartRequired = confirmedMode != a.opts.PermissionMode()
			if rejectedPlan {
				a.nativeInputRestartRequired = true
			}
		}
		a.mu.Unlock()
		if confirmedMode != "" {
			a.sink.UpdatePermissionMode(confirmedMode)
		}
	}
	return turnRejected
}

// CodeBuddy can omit streamed text after reasoning, while result.result still
// holds the answer. Publish that answer once before the turn-end frame.
func (a *Agent) backfillMissingAssistantText(result resultMessage) {
	if result.IsError {
		return
	}
	var answer string
	if json.Unmarshal(result.Result, &answer) != nil || strings.TrimSpace(answer) == "" {
		return
	}
	a.mu.Lock()
	streamed := a.assistantTextStreamed
	sessionID := a.sessionID
	a.mu.Unlock()
	if streamed {
		return
	}
	if result.SessionID != "" {
		sessionID = result.SessionID
	}
	frame, err := json.Marshal(map[string]any{
		"type":       contracts.CodebuddyFrameKindAssistant,
		"session_id": sessionID,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]string{{"type": "text", "text": answer}},
		},
	})
	if err != nil {
		slog.Warn("codebuddy: encode missing assistant answer", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.persistRaw(frame)
}

// codebuddyContextUsage reads the primary model's counts and current context size.
func codebuddyContextUsage(result resultMessage, model string) map[string]any {
	var selected *codebuddyModelUsage
	if usage, ok := result.ModelUsage[model]; ok {
		selected = &usage
	} else if len(result.ModelUsage) == 1 {
		for _, usage := range result.ModelUsage {
			selected = &usage
		}
	}
	if selected == nil && result.Usage == nil && result.Meta.ContextUsed == nil {
		return nil
	}

	counts := providerkit.ContextTokenCounts{}
	if result.Usage != nil {
		counts = providerkit.ContextTokenCounts{
			Input:      max(0, result.Usage.InputTokens),
			Output:     max(0, result.Usage.OutputTokens),
			CacheWrite: max(0, result.Usage.CacheCreationInputTokens),
			CacheRead:  max(0, result.Usage.CacheReadInputTokens),
		}
	}
	if selected != nil {
		counts = providerkit.ContextTokenCounts{
			Input:      max(0, selected.InputTokens),
			Output:     max(0, selected.OutputTokens),
			CacheWrite: max(0, selected.CacheCreationInputTokens),
			CacheRead:  max(0, selected.CacheReadInputTokens),
		}
	}
	usage := providerkit.ContextUsageMap(counts)
	if selected != nil && selected.ContextWindow > 0 {
		usage[contracts.ContextUsageFieldContextWindow] = selected.ContextWindow
	}
	if result.Meta.ContextUsed != nil && *result.Meta.ContextUsed >= 0 {
		usage[contracts.ContextUsageFieldContextTokens] = *result.Meta.ContextUsed
	}
	return usage
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
		Answered: true,
		Success:  envelope.Response.Subtype == "success",
		Error:    envelope.Response.Error,
	}
	var inner struct {
		Mode            string                   `json:"mode"`
		Model           string                   `json:"model"`
		AvailableModels *[]codebuddyModelInfo    `json:"availableModels"`
		OutputStyle     string                   `json:"output_style"`
		OutputStyles    *[]string                `json:"available_output_styles"`
		ConfigUpdated   *codebuddyNativeSettings `json:"updated"`
		ConfigErrors    map[string]string        `json:"errors"`
		Steered         *bool                    `json:"steered"`
		Reason          string                   `json:"reason"`
	}
	validInner := true
	if len(envelope.Response.Response) > 0 {
		if err := json.Unmarshal(envelope.Response.Response, &inner); err != nil {
			validInner = false
			result.Success = false
			if result.Error == "" {
				result.Error = "CodeBuddy sent an invalid control response."
			}
			slog.Debug("read the CodeBuddy control payload", "agent_id", a.AgentID(), "error", err)
		}
	}
	if validInner {
		result.ConfigUpdated = inner.ConfigUpdated
		result.ConfigErrors = inner.ConfigErrors
		result.OutputStyle = inner.OutputStyle
		if inner.OutputStyles != nil {
			result.OutputStyles = *inner.OutputStyles
			result.HasOutputStyleCatalog = true
		}
		result.Mode = inner.Mode
		result.Model = inner.Model
		if inner.AvailableModels != nil {
			result.Models = *inner.AvailableModels
			result.HasModelCatalog = true
		}
		result.Steered = inner.Steered
		result.SteerReason = inner.Reason
	}
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

// codebuddyControlTimerTag labels the timers of sendControlAndWait. The timers
// come from the clock of the process, so a test that drives a mock clock ends
// the wait.
const codebuddyControlTimerTag = "codebuddy control response"

// codebuddyControlExitWait is how long sendControlAndWait waits for the process
// exit after a failed write, so that the error states the exit when the write
// failed because the process ended.
const codebuddyControlExitWait = time.Second

// sendControlAndWait sends a control request and waits for its response.
//
// The result reports whether CodeBuddy answered (codebuddyControlResult.Answered).
// An error answer returns the result together with the error of CodeBuddy. A
// failed write, a process exit and a timeout return an unanswered result.
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
	if err := a.SendRawInput([]byte(msg), agent.StopContext{}); err != nil {
		exitWait := a.Clock().NewTimer(codebuddyControlExitWait, codebuddyControlTimerTag)
		defer exitWait.Stop(codebuddyControlTimerTag)
		select {
		case <-a.ProcessDone():
			return codebuddyControlResult{}, a.ProcessExitError()
		case <-exitWait.C:
			return codebuddyControlResult{}, err
		}
	}
	answerWait := a.Clock().NewTimer(timeout, codebuddyControlTimerTag)
	defer answerWait.Stop(codebuddyControlTimerTag)
	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, errString(resp.Error)
		}
		return resp, nil
	case <-a.ProcessDone():
		return codebuddyControlResult{}, a.ProcessExitError()
	case <-answerWait.C:
		return codebuddyControlResult{}, errControlTimeout
	}
}

// applyPermissionMode sends set_permission_mode and records the result.
func (a *Agent) applyPermissionMode(mode string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("CodeBuddy session is not initialized")
	}
	body, err := json.Marshal(map[string]string{
		"subtype":    contracts.CodebuddyControlRequestSubtypeSetPermissionMode,
		"mode":       mode,
		"session_id": sessionID,
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
	a.nativeModeRestartRequired = false
	a.mu.Unlock()
	a.queuePermissionApprovals()
	return nil
}

// persistUnknown preserves an unknown frame only for this process's current session.
func (a *Agent) persistUnknown(raw []byte) {
	sessionID := providerkit.StringField(raw, "session_id")
	a.mu.Lock()
	current := a.sessionID
	a.mu.Unlock()
	if sessionID != "" && sessionID != current {
		slog.Debug("CodeBuddy ignored an unknown frame from another session", "agent_id", a.AgentID(), "session_id", sessionID)
		return
	}
	a.persistRaw(raw)
}

// persistRaw forwards one verbatim NDJSON frame to the transcript.
func (a *Agent) persistRaw(raw []byte) {
	var frame struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         struct {
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
	}
	decoded := json.Unmarshal(raw, &frame) == nil
	info := agent.SpanInfo{}
	if decoded {
		for _, block := range frame.Message.Content {
			if frame.Type == contracts.CodebuddyFrameKindAssistant && block.Type == "tool_use" && strings.TrimSpace(block.ID) != "" && block.Name != "" {
				info = agent.SpanInfo{SpanID: block.ID, SpanType: block.Name, ParentSpanID: frame.ParentToolUseID, SpanColor: a.sink.ReserveSpanColor(block.ID, frame.ParentToolUseID)}
				break
			}
			if frame.Type == contracts.CodebuddyFrameKindUser && block.Type == "tool_result" && strings.TrimSpace(block.ToolUseID) != "" {
				info = agent.SpanInfo{SpanID: block.ToolUseID, SpanType: a.sink.GetSpanType(block.ToolUseID), ParentSpanID: frame.ParentToolUseID, Closing: true}
				break
			}
		}
	}
	content := agent.MessageContent{Original: raw}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, info); err != nil {
		slog.Debug("codebuddy: persist message", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !decoded {
		return
	}
	seen := make(map[string]struct{})
	for _, block := range frame.Message.Content {
		if frame.Type == contracts.CodebuddyFrameKindAssistant && block.Type == "tool_use" && strings.TrimSpace(block.ID) != "" && block.Name != "" {
			if _, duplicate := seen[block.ID]; duplicate {
				continue
			}
			seen[block.ID] = struct{}{}
			a.sink.SetSpanType(block.ID, block.Name)
			a.sink.OpenSpan(block.ID, frame.ParentToolUseID)
		}
		if frame.Type == contracts.CodebuddyFrameKindUser && block.Type == "tool_result" && strings.TrimSpace(block.ToolUseID) != "" {
			if _, duplicate := seen[block.ToolUseID]; duplicate {
				continue
			}
			seen[block.ToolUseID] = struct{}{}
			a.sink.CloseSpan(block.ToolUseID)
		}
	}
}

// HandleOutput processes a single NDJSON line from CodeBuddy. It runs the same
// pipeline as the reader loop: a pending control_response is answered first,
// and only an unconsumed line reaches handleOutput.
func (a *Agent) HandleOutput(content []byte) {
	line := &providerkit.ParsedLine{Raw: content, Type: string(envelopeType(content))}
	if a.handlePendingControlResponse(line) {
		return
	}
	a.handleOutput(line)
}

// envelopeType extracts the top-level `type` of one frame.
func envelopeType(content []byte) MessageType {
	var envelope MessageEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return ""
	}
	return envelope.Type
}
