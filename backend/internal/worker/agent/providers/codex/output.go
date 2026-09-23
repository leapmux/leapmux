package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var codexRetryableDisconnectPattern = regexp.MustCompile(`^stream disconnected before completion(?:$|[^[:alnum:]].*)`)

const codexMultiAgentV2Namespace = "collaboration"
const codexCollabToolSpawnAgent = "spawnAgent"

// codexSystemMetadataMethods contains Codex JSON-RPC notifications that the
// transcript stores as agent metadata. Methods that need extra state changes
// use dedicated cases below. An unhandled method reaches the default case and
// lands in the transcript as a raw JSON-RPC row.
var codexSystemMetadataMethods = map[string]struct{}{
	contracts.CodexMethodThreadCompacted:   {},
	contracts.CodexMethodThreadNameUpdated: {},
}

// handleCodexOutput processes a single parsed JSONL notification from the Codex app-server.
// Codex messages are stored in their native JSON-RPC format.
func handleCodexOutput(a *Agent, line *providerkit.ParsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.isRetiredCodexOutput(line.Params) {
		return
	}
	slog.Debug("codex HandleOutput", "agent_id", a.AgentID(), "method", line.Method, "len", len(line.Raw))

	if _, ok := codexSystemMetadataMethods[line.Method]; ok {
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw); err != nil {
			slog.Error("codex persist system metadata", "agent_id", a.AgentID(), "method", line.Method, "error", err)
		}
		return
	}

	switch line.Method {
	case contracts.CodexMethodTurnStarted:
		a.handleTurnStarted(line.Params)

	case "item/agentMessage/delta":
		a.handleAgentMessageDelta(line.Params)

	case "item/plan/delta":
		a.handlePlanDelta(line.Params)

	case "item/reasoning/summaryTextDelta":
		a.handleReasoningSummaryTextDelta(line.Params)

	case "item/reasoning/summaryPartAdded":
		a.handleReasoningSummaryPartAdded(line.Params)

	case "item/reasoning/textDelta":
		a.handleReasoningTextDelta(line.Params)

	case "item/commandExecution/outputDelta":
		a.handleCommandExecutionOutputDelta(line.Params)

	case "item/commandExecution/terminalInteraction":
		a.handleCommandExecutionTerminalInteraction(line.Params)

	case "item/fileChange/outputDelta":
		a.handleFileChangeOutputDelta(line.Params)

	case contracts.CodexMethodItemStarted:
		a.handleItemStarted(line.Raw, line.Params)

	case contracts.CodexMethodItemCompleted:
		a.handleItemCompleted(line.Raw, line.Params)

	case "turn/completed":
		a.handleTurnCompleted(line.Params)

	case contracts.CodexMethodThreadTokenUsageUpdated:
		a.handleTokenUsageUpdated(line.Params)

	case contracts.CodexMethodMcpToolCallProgress:
		// The item itself carries the finished result. Persisting progress creates
		// unrelated raw rows and gives the transcript no failure information.

	case contracts.CodexMethodMcpServerOauthLoginCompleted:
		a.handleMcpOauthLoginCompleted(line.Raw, line.Params)

	case contracts.CodexMethodMcpServerStartupStatusUpdated:
		a.handleMcpStartupStatusUpdated(line.Raw, line.Params)

	case contracts.CodexMethodRawResponseItemCompleted:
		if !a.handleRawResponseItemCompleted(line.Params) {
			a.persistUnknownCodexNotification(line)
		}

	case contracts.CodexMethodThreadStatusChanged:
		// turn/started and turn/completed own the Worker's turn state. Codex sends
		// this second lifecycle view around the same turn, so it adds no state for
		// the transcript, the thinking indicator, or turn-end detection.

	case contracts.CodexMethodSkillsChanged:
		// Skill discovery state does not describe a transcript row or LeapMux
		// session state.

	case contracts.CodexMethodRemoteControlStatusChanged:
		// Codex remote control reports app-server transport state. It does not
		// describe the LeapMux session, turn, or control channel.

	case contracts.CodexMethodHookStarted:
		// A hook start has no outcome and no transcript content. Keep the matching
		// unsuccessful completion below, which carries the failure details.

	case contracts.CodexMethodHookCompleted:
		a.handleHookCompleted(line.Raw, line.Params)

	case codexMethodThreadSettingsUpdated:
		a.handleThreadSettingsUpdated(line.Params)

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case contracts.MCPElicitationMethodCodex:
		a.PublishControlRequest(a.sink, line.Raw, providerkit.MCPElicitationCancelAnswer())

	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"item/tool/requestUserInput":
		// Codex retires its own approval requests through serverRequest/resolved, and it
		// defines no outcome for one the client withdraws, so LeapMux sends no answer.
		a.PublishControlRequest(a.sink, line.Raw, nil)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(line.Params)

	case "account/rateLimits/updated":
		a.handleRateLimitsUpdated(line.Raw, line.Params)

	// The session goal. These are claimed out of the `default:` case below on
	// purpose: Codex reports the goal after EVERY completed tool call, so the
	// fallback wrote a raw-JSON row per tool call, and each of those rows also
	// broke notification adjacency. The sink keeps the goal as session state and
	// writes the transcript only when the goal actually changes.
	case codexMethodGoalUpdated:
		a.handleGoalUpdated(line.Params)

	case codexMethodGoalCleared:
		a.handleGoalCleared(line.Params)

	case "error":
		a.handleErrorNotification(line.Params)

	default:
		// An inbound REQUEST needs an answer. The comment above states that Codex
		// sends its approval requests with an "id", so one whose method this
		// dispatcher does not recognize reaches here carrying a runtime that waits.
		// A transcript row alone leaves it waiting for its own timeout.
		a.RefuseUnsupportedRequest(line)
		a.persistUnknownCodexNotification(line)
	}
}

func (a *Agent) persistUnknownCodexNotification(line *providerkit.ParsedLine) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw}, agent.SpanInfo{}); err != nil {
		slog.Error("codex persist notification", "agent_id", a.AgentID(), "method", line.Method, "error", err)
	}
}

// handleTurnStarted processes turn/started notifications.
//
// Resets per-turn state and broadcasts the new turn ID so the frontend
// can wire up interrupt. Git status is refreshed automatically at
// turn-end by the sink layer.
func (a *Agent) handleTurnStarted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &notif) != nil || notif.Turn.ID == "" {
		return
	}
	a.clearReasoningStateForThread(notif.ThreadID)
	a.clearInterruptCallsForThread(notif.ThreadID)
	if a.isMainThreadID(notif.ThreadID) {
		a.handleMainTurnStarted(notif.Turn.ID)
		return
	}
	route, routed := a.ensureCodexChildRoute(notif.ThreadID)
	if !routed {
		a.enqueuePendingCodexChildEvent(notif.ThreadID, codexPendingChildEvent{
			kind:   codexPendingTurnStarted,
			params: append(json.RawMessage(nil), params...),
		})
		return
	}
	a.replayPendingCodexChildEvents(notif.ThreadID, route)
	a.handleChildTurnStarted(notif.ThreadID, notif.Turn.ID, route)
}

func (a *Agent) handleMainTurnStarted(turnID string) {
	a.Mu.Lock()
	if a.turnStartAck != nil {
		close(a.turnStartAck)
		a.turnStartAck = nil
	}
	a.turnID = turnID
	a.TurnToolUses = 0
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetModelProgress())
}

func (a *Agent) handleChildTurnStarted(threadID, turnID string, route codexChildRoute) {
	a.activateCollabChild(threadID)
	a.setChildTurnID(threadID, turnID)
	a.flushCodexChildGeneration(threadID, agent.MessageCompletionInterrupted)
	route.childSink.ReportProgress(agent.ResetModelProgress())
	_, _, err := a.upsertCodexChildRegistryRow(threadID, codexChildTransition{
		status:   bgtask.StatusRunning,
		activity: "working",
	})
	providerkit.LogRegistryRefusal("codex", "upsert", err)
	a.publishCodexChildTurnActive(route.childSink, true)
}

type codexItemEvent struct {
	raw      json.RawMessage
	params   json.RawMessage
	item     json.RawMessage
	itemType string
	itemID   string
	threadID string
}

func newCodexItemEvent(raw []byte, params json.RawMessage) (codexItemEvent, bool) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return codexItemEvent{}, false
	}
	return codexItemEvent{
		raw:      raw,
		params:   params,
		item:     item,
		itemType: itemType,
		itemID:   itemID,
		threadID: threadID,
	}, true
}

// handleItemStarted processes item/started notifications.
func (a *Agent) handleItemStarted(raw []byte, params json.RawMessage) {
	event, ok := newCodexItemEvent(raw, params)
	if !ok {
		return
	}
	// subAgentActivity (v2) is registry-only: never persist. Consume it here
	// before any transcript handling.
	if event.itemType == "subAgentActivity" {
		a.handleCodexSubAgentActivity(event.item, event.threadID)
		return
	}

	if !a.isMainThreadID(event.threadID) {
		if codexItemIsTool(event.itemType) {
			a.rememberCodexIncompleteTool(event.itemID, event.itemType, event.threadID, event.params)
			a.rememberCodexChildItemThread(event.itemID, event.threadID)
		}
		route, routed := a.ensureCodexChildRoute(event.threadID)
		if !routed {
			a.enqueuePendingCodexChildEvent(event.threadID, codexPendingChildEvent{
				kind:   codexPendingItemStarted,
				raw:    append(json.RawMessage(nil), event.raw...),
				params: append(json.RawMessage(nil), event.params...),
			})
			return
		}
		a.replayPendingCodexChildEvents(event.threadID, route)
		a.handleCodexItemStartedForSink(route.childSink, route.agentID, false, event)
		return
	}
	a.handleCodexItemStartedForSink(a.sink, a.AgentID(), true, event)
}

func (a *Agent) handleCodexItemStartedForSink(
	sink agent.ProviderServices,
	agentID string,
	mainThread bool,
	event codexItemEvent,
) {
	if codexItemIsTool(event.itemType) {
		ownerThreadID := event.threadID
		if mainThread {
			ownerThreadID = ""
		}
		a.rememberCodexIncompleteTool(event.itemID, event.itemType, ownerThreadID, event.params)
	}
	switch event.itemType {
	case contracts.CodexItemTypeAgentMessage:
		// Wait for the authoritative completed item.
	case contracts.CodexItemTypeContextCompaction:
		if mainThread {
			a.Mu.Lock()
			if a.compactionStartAck != nil {
				close(a.compactionStartAck)
				a.compactionStartAck = nil
			}
			a.Mu.Unlock()
		}
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
			slog.Error("codex persist compacting notification", "agent_id", agentID, "error", err)
		}
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView, contracts.CodexItemTypeReasoning:
		persistSharedItemStarted(sink, event, agentID)
	case contracts.CodexItemTypeCollabAgentToolCall:
		collab := parseCollabToolCall(event.item)
		spawns := collab != nil && collab.Tool == codexCollabToolSpawnAgent
		if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: event.params}, event.itemID, event.itemType, spawns); err != nil {
			slog.Error("codex persist collabAgentToolCall/started", "agent_id", agentID, "error", err)
		}
		a.registerCollabReceivers(collab, event.itemID, event.threadID)
	}
}

// handleItemCompleted processes item/completed notifications.
func (a *Agent) handleItemCompleted(raw []byte, params json.RawMessage) {
	event, ok := newCodexItemEvent(raw, params)
	if !ok {
		return
	}
	// subAgentActivity (v2) is registry-only: never persist. Consume it here
	// before any transcript handling.
	if event.itemType == "subAgentActivity" {
		a.handleCodexSubAgentActivity(event.item, event.threadID)
		return
	}

	if !a.isMainThreadID(event.threadID) {
		route, routed := a.ensureCodexChildRoute(event.threadID)
		if !routed {
			a.enqueuePendingCodexChildEvent(event.threadID, codexPendingChildEvent{
				kind:   codexPendingItemCompleted,
				raw:    append(json.RawMessage(nil), event.raw...),
				params: append(json.RawMessage(nil), event.params...),
			})
			return
		}
		a.replayPendingCodexChildEvents(event.threadID, route)
		a.handleCodexItemCompletedForSink(route.childSink, route.agentID, false, event)
		a.persistCodexChildReport(route, event)
		return
	}
	a.handleCodexItemCompletedForSink(a.sink, a.AgentID(), true, event)
}

func (a *Agent) persistCodexChildReport(route codexChildRoute, event codexItemEvent) {
	if event.itemType != contracts.CodexItemTypeAgentMessage {
		return
	}
	var message struct {
		Text  string `json:"text"`
		Phase string `json:"phase"`
	}
	if json.Unmarshal(event.item, &message) != nil || strings.TrimSpace(message.Text) == "" {
		return
	}
	label, fresh := a.recordCodexChildReportCandidate(event.threadID, event.itemID, message.Text, message.Phase == "final_answer")
	if fresh {
		providerkit.PersistSubagentReport(route.parentSink, agent.SubagentReportWrite{
			ReportID: event.itemID,
			Report:   agent.SubagentReport{Label: label, Text: message.Text},
		})
	}
}

func (a *Agent) handleCodexItemCompletedForSink(
	sink agent.ProviderServices,
	agentID string,
	mainThread bool,
	event codexItemEvent,
) {
	a.Mu.Lock()
	delete(a.incompleteTools, event.itemID)
	delete(a.collabChildItems, event.itemID)
	a.Mu.Unlock()
	if mainThread {
		discardCompletedCodexGeneration(&a.generationBuffer, event.itemType, event.itemID)
	} else {
		discardCompletedCodexGeneration(a.codexChildGenerationBuffer(event.threadID), event.itemType, event.itemID)
	}

	switch event.itemType {
	case contracts.CodexItemTypeAgentMessage:
		persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
	case contracts.CodexItemTypePlan:
		if !mainThread {
			persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
			return
		}
		a.Mu.Lock()
		a.turnSawPlan = true
		a.Mu.Unlock()
		sink.ReportProgress(agent.CompleteModelProgress(event.itemID))

		var planItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.item, &planItem) == nil && planItem.Text != "" {
			a.Mu.Lock()
			a.turnPlanText = planItem.Text
			a.Mu.Unlock()
		}
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.params}, agent.SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType,
		}); err != nil {
			slog.Error("codex persist plan", "agent_id", agentID, "error", err)
		}
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView:
		if mainThread {
			a.Mu.Lock()
			a.TurnToolUses++
			a.Mu.Unlock()
		}
		persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
	case contracts.CodexItemTypeCollabAgentToolCall:
		collab := parseCollabToolCall(event.item)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.params}, agent.SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/completed", "agent_id", agentID, "error", err)
		}
		sink.CloseSpan(event.itemID)
		if collab != nil {
			a.registerCollabReceivers(collab, event.itemID, event.threadID)
			a.collabAgentsStatesToRegistry(collab)
		}
	case contracts.CodexItemTypeReasoning:
		a.persistCompletedReasoningItem(sink, event, agentID)
	case contracts.CodexItemTypeContextCompaction:
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
			slog.Error("codex persist contextCompaction/completed", "agent_id", agentID, "error", err)
		}
	default:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.params}, agent.SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType,
		}); err != nil {
			slog.Error("codex persist unknown item", "agent_id", agentID, "type", event.itemType, "error", err)
		}
	}
}

// handleTurnCompleted processes turn/completed notifications.
func (a *Agent) handleTurnCompleted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &notif) == nil && !a.isMainThreadID(notif.ThreadID) {
		route, routed := a.ensureCodexChildRoute(notif.ThreadID)
		if !routed {
			a.enqueuePendingCodexChildEvent(notif.ThreadID, codexPendingChildEvent{
				kind:   codexPendingTurnCompleted,
				params: append(json.RawMessage(nil), params...),
			})
			return
		}
		a.replayPendingCodexChildEvents(notif.ThreadID, route)
		a.handleChildTurnCompleted(notif.ThreadID, params, route)
		return
	}

	completion := codexTurnCompletion(params)
	a.flushCodexGeneration(completion)
	incompleteToolUses := a.persistIncompleteCodexTools("", false, completion)

	// Enrich the params with num_tool_uses so the frontend can distinguish
	// simple text-only exchanges from complex multi-tool turns.
	a.Mu.Lock()
	numToolUses := a.TurnToolUses + incompleteToolUses
	sawPlan := a.turnSawPlan
	planText := a.turnPlanText
	collaborationMode := a.collaborationMode
	a.Mu.Unlock()

	// Read turn data without changing the native parameters.
	var turnStatus, turnID, turnErrorMessage, turnErrorInfo string
	parsed := make(map[string]json.RawMessage)
	if err := json.Unmarshal(params, &parsed); err == nil {
		if turnRaw, ok := parsed["turn"]; ok {
			var turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  struct {
					Message        string `json:"message"`
					CodexErrorInfo string `json:"codexErrorInfo"`
				} `json:"error"`
			}
			if json.Unmarshal(turnRaw, &turn) == nil {
				turnStatus = turn.Status
				turnID = turn.ID
				turnErrorMessage = turn.Error.Message
				turnErrorInfo = turn.Error.CodexErrorInfo
			}
		}
	}
	// Clear provider turn state before the deferred publish below releases the
	// Worker's input queue. This lets the next queued input start a new turn.
	a.Mu.Lock()
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	clear(a.codexSpawnPrompts)
	a.Mu.Unlock()
	// Deferred, so it lands AFTER the PersistTurnEnd below. That call hands the
	// finished turn's tool-call count to the Worker's activity latch, and the
	// clear published here is the settle edge that spends it -- publishing at
	// the assignment above would settle the agent with no count and ring the
	// completion sound for a turn that used no tool. The turn state itself
	// still clears early, which is what lets the next queued input start a turn.
	//
	// The publish runs INLINE on this reader goroutine, although it reaches the
	// input queue's store. That is safe because Manager.drain never holds the
	// coordinator lock across dispatcher.Dispatch, so nothing an in-flight RPC
	// waits for can hold what this publish needs. An earlier version escaped to
	// a new goroutine for that reason, and the escape reordered the clear past
	// the next turn's start.
	defer a.PublishTurnActive()
	a.clearInterruptCallsForThread(notif.ThreadID)

	// Persist as a result divider.
	if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(agent.MessageContent{Original: params}, numToolUses), agent.SpanInfo{}); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.AgentID(), "error", err)
	}

	// Reset all span tracking at turn-end so the next turn starts clean.
	// The child routes stay here because background tasks outlive root turns.
	// A completed child run keeps its route for later input. ClearContext or a
	// process restart removes the routes for the old child tree.
	a.sink.ResetSpans()

	if turnStatus != "" {
		retryable := turnStatus == "failed" &&
			(turnErrorInfo == "serverOverloaded" || isRetryableCodexTurnFailure(turnErrorMessage))
		providerkit.ScheduleOrCancelAPIErrorAutoContinue(a.sink, retryable, params)
		if turnStatus == "completed" && collaborationMode == CollaborationPlan && sawPlan && planText != "" {
			// Persist plan content so initiatePlanExecution can use it.
			compressed, compression := msgcodec.Compress([]byte(planText))
			a.sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(planText))
			requestID := fmt.Sprintf("codex-plan-prompt-%s", turnID)
			// One plan is stored for each agent, and the UpdatePlan above just
			// REPLACED it. A card from an earlier turn identifies the plan the reader
			// saw and would execute THIS one, so it is invalid from this line on --
			// whether or not the new card publishes. Retiring it only on a successful
			// publish left a publish failure with a live card pointing at a plan that
			// is gone; the composer renders the OLDEST card, so approving what the
			// reader could see executed a plan it never showed.
			//
			// CancelControlRequest returns in silence for a row that is already gone,
			// so an answered card costs nothing. Nothing else can retire this request:
			// it is LeapMux's own, it carries no JSON-RPC id, and so the
			// outstanding-control registrar never held it.
			a.Mu.Lock()
			superseded := a.planPromptRequestID
			a.planPromptRequestID = requestID
			a.Mu.Unlock()
			if superseded != "" && superseded != requestID {
				a.sink.CancelControlRequest(superseded)
			}
			payload, err := json.Marshal(map[string]interface{}{
				"type":       "control_request",
				"request_id": requestID,
				"request": map[string]interface{}{
					"tool_name": ToolNamePlanModePrompt,
					"input":     map[string]interface{}{},
				},
			})
			if err == nil {
				if err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: payload}); err != nil {
					slog.Error("publish plan approval", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
				}
			}
		}
	}
}

func (a *Agent) handleChildTurnCompleted(threadID string, params json.RawMessage, route codexChildRoute) {
	completion := codexTurnCompletion(params)
	a.flushCodexChildGeneration(threadID, completion)
	a.persistIncompleteCodexTools(threadID, false, completion)
	if reportID, label, report, ok := a.takeCodexChildReportCandidate(threadID); ok {
		providerkit.PersistSubagentReport(route.parentSink, agent.SubagentReportWrite{
			ReportID: reportID,
			Report:   agent.SubagentReport{Label: label, Text: report},
		})
	}
	if err := route.childSink.PersistTurnEnd(agent.MessageContent{Original: params}, agent.SpanInfo{}); err != nil {
		slog.Warn("codex persist child turn/completed", "agent_id", a.AgentID(), "thread", threadID, "error", err)
	}
	hadTurn := a.childTurnID(threadID) != ""
	a.clearChildTurnID(threadID)
	if hadTurn {
		a.publishCodexChildTurnActive(route.childSink, false)
	}
	transition := codexChildTurnTransition(params)
	if transition.finished() {
		a.completeCodexChildRun(threadID, transition)
	} else if transition.activity != "" {
		providerkit.LogRegistryRefusal("codex", "update status",
			a.sink.UpdateBackgroundTaskStatus(threadID, bgtask.StatusRunning, transition.activity))
	}
}

func (a *Agent) flushCodexGeneration(completion agent.MessageCompletion) {
	a.persistCodexGeneration(&a.generationBuffer, a.sink, completion)
}

func (a *Agent) flushCodexChildGeneration(threadID string, completion agent.MessageCompletion) {
	route, ok := a.lookupCodexChildRoute(threadID)
	if !ok {
		return
	}
	a.persistCodexGeneration(a.codexChildGenerationBuffer(threadID), route.childSink, completion)
}

func (a *Agent) flushAllCodexGeneration(completion agent.MessageCompletion) {
	a.flushCodexGeneration(completion)
	a.Mu.Lock()
	threadIDs := make([]string, 0, len(a.collabChildren))
	for threadID, state := range a.collabChildren {
		if state != nil && state.childAgentID != "" {
			threadIDs = append(threadIDs, threadID)
		}
	}
	a.Mu.Unlock()
	sort.Strings(threadIDs)
	for _, threadID := range threadIDs {
		a.flushCodexChildGeneration(threadID, completion)
	}
}

func (a *Agent) persistCodexGeneration(buffer *providerkit.GenerationBuffer, sink providerkit.GenerationServices, completion agent.MessageCompletion) {
	if a.IsDiscardingOutput() {
		buffer.Reset()
		sink.ReportProgress(agent.ResetModelProgress())
		return
	}
	if err := buffer.PersistAll(completion, func(raw []byte) error {
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	}); err != nil {
		slog.Error("codex persist partial generation", "agent_id", a.AgentID(), "error", err)
	}
}

func codexTurnCompletion(params json.RawMessage) agent.MessageCompletion {
	var value struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &value) != nil {
		return agent.MessageCompletionError
	}
	switch strings.ToLower(value.Turn.Status) {
	case "completed":
		return agent.MessageCompletionComplete
	case "cancelled", "canceled", "interrupted", "aborted":
		return agent.MessageCompletionInterrupted
	default:
		return agent.MessageCompletionError
	}
}

func isRetryableCodexTurnFailure(message string) bool {
	return codexRetryableDisconnectPattern.MatchString(message)
}

// handleTokenUsageUpdated processes thread/tokenUsage/updated notifications.
func (a *Agent) handleTokenUsageUpdated(params json.RawMessage) {
	var notif struct {
		ThreadID   string `json:"threadId"`
		TurnID     string `json:"turnId"`
		TokenUsage struct {
			// `last` is the LIVE context: what the most recent request carried.
			// The sibling `total` bucket is the session's CUMULATIVE spend, which
			// grows past the context window and answers a different question, so
			// the context gauge never reads it.
			Last struct {
				TotalTokens           int64 `json:"totalTokens"`
				InputTokens           int64 `json:"inputTokens"`
				CachedInputTokens     int64 `json:"cachedInputTokens"`
				CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
				OutputTokens          int64 `json:"outputTokens"`
				ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
			} `json:"last"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex token_usage_updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}

	// Codex reports the cached part inside InputTokens, so the uncached remainder
	// is the input the other providers report.
	//
	// CacheWrite is reported as Codex states it, with nothing subtracted from the
	// input beside it. Whether `cacheWriteInputTokens` is a subset of `inputTokens`
	// -- as `cachedInputTokens` is -- is not stated anywhere in the protocol, and a
	// probe of the installed 0.154.0 could not settle it. Subtracting on a guess
	// would under-report the input on one reading and the gauge does not depend on
	// the answer, because ContextTokens below is authoritative for it.
	//
	// ReasoningOutputTokens is deliberately NOT a fifth count. The struct mirrors
	// the OpenAI Responses shape, where the reasoning tokens are a DETAIL OF the
	// output tokens rather than a bucket beside them, so a count of its own would
	// add the same tokens twice.
	usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:      max(notif.TokenUsage.Last.InputTokens-notif.TokenUsage.Last.CachedInputTokens, 0),
		CacheWrite: notif.TokenUsage.Last.CacheWriteInputTokens,
		CacheRead:  notif.TokenUsage.Last.CachedInputTokens,
		Output:     notif.TokenUsage.Last.OutputTokens,
	})
	// Codex's OWN total for the live request. The browser prefers a provider-reported
	// total to the sum of the four counts (`contextSize`), so the gauge states what
	// Codex measured rather than a total LeapMux derived -- which is what makes the
	// unanswered subset question above harmless.
	if notif.TokenUsage.Last.TotalTokens > 0 {
		usage[contracts.ContextUsageFieldContextTokens] = notif.TokenUsage.Last.TotalTokens
	}
	if notif.TokenUsage.ModelContextWindow != nil {
		usage[contracts.ContextUsageFieldContextWindow] = *notif.TokenUsage.ModelContextWindow
	} else if cw := agent.FindAvailableModel(a.availableModels, a.model).GetContextWindow(); cw > 0 {
		usage[contracts.ContextUsageFieldContextWindow] = cw
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyContextUsage: usage,
	})
}

// handleHookCompleted drops successful outcomes and routes every other state.
func (a *Agent) handleHookCompleted(content []byte, params json.RawMessage) {
	var notification struct {
		ThreadID string `json:"threadId"`
		Run      struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	if json.Unmarshal(params, &notification) == nil && notification.Run.Status == "completed" {
		return
	}
	a.persistCodexThreadFailure(notification.ThreadID, codexPendingHookCompleted, content, params)
}

// handleMcpOauthLoginCompleted keeps only failures and unknown future outcomes.
func (a *Agent) handleMcpOauthLoginCompleted(content []byte, params json.RawMessage) {
	var notification struct {
		ThreadID string `json:"threadId"`
		Success  *bool  `json:"success"`
	}
	if json.Unmarshal(params, &notification) == nil && notification.Success != nil && *notification.Success {
		return
	}
	a.persistCodexThreadFailure(notification.ThreadID, codexPendingMcpOauthCompleted, content, params)
}

// handleMcpStartupStatusUpdated keeps only failures and unknown future states.
func (a *Agent) handleMcpStartupStatusUpdated(content []byte, params json.RawMessage) {
	switch codexMcpStartupState(params) {
	case "starting", "ready", "cancelled":
		return
	}
	var notification struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &notification)
	a.persistCodexThreadFailure(notification.ThreadID, codexPendingMcpStartupUpdated, content, params)
}

func (a *Agent) persistCodexThreadFailure(threadID string, kind codexPendingChildEventKind, content, params json.RawMessage) {
	if a.isMainThreadID(threadID) {
		a.persistCodexFailureForSink(a.sink, a.AgentID(), kind, content)
		return
	}
	route, routed := a.ensureCodexChildRoute(threadID)
	if !routed {
		a.enqueuePendingCodexChildEvent(threadID, codexPendingChildEvent{
			kind: kind, raw: append(json.RawMessage(nil), content...), params: append(json.RawMessage(nil), params...),
		})
		return
	}
	a.replayPendingCodexChildEvents(threadID, route)
	a.persistCodexFailureForSink(route.childSink, route.agentID, kind, content)
}

func (a *Agent) persistCodexFailureForSink(sink agent.ProviderServices, agentID string, kind codexPendingChildEventKind, content json.RawMessage) {
	if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("codex persist failure notification", "agent_id", agentID, "kind", kind, "error", err)
	}
}

func (a *Agent) handleRawResponseItemCompleted(params json.RawMessage) bool {
	var notification struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Arguments string `json:"arguments"`
			CallID    string `json:"call_id"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &notification) != nil || !a.isMainThreadID(notification.ThreadID) ||
		notification.Item.Type != "function_call" || notification.Item.Name != "spawn_agent" ||
		notification.Item.Namespace != codexMultiAgentV2Namespace || notification.Item.CallID == "" {
		return false
	}
	var arguments struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(notification.Item.Arguments), &arguments) != nil {
		return false
	}
	a.rememberCodexSpawnPrompt(notification.Item.CallID, arguments.Message)
	return true
}

func codexMcpStartupState(params json.RawMessage) string {
	var notification struct {
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return ""
	}
	var state string
	if json.Unmarshal(notification.Status, &state) == nil {
		return state
	}
	var nested struct {
		State string `json:"state"`
	}
	if json.Unmarshal(notification.Status, &nested) == nil {
		return nested.State
	}
	return ""
}

func (a *Agent) isMainThreadID(threadID string) bool {
	if threadID == "" {
		return true
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return threadID == a.threadID
}

func (a *Agent) isRetiredCodexOutput(params json.RawMessage) bool {
	var value struct {
		ThreadID string `json:"threadId"`
	}
	return json.Unmarshal(params, &value) == nil && a.isRetiredCodexThread(value.ThreadID)
}

func (a *Agent) isRetiredCodexThread(threadID string) bool {
	if threadID == "" {
		return false
	}
	a.Mu.Lock()
	_, retired := a.retiredCodexThreads[threadID]
	a.Mu.Unlock()
	return retired
}

// handleServerRequestResolved processes serverRequest/resolved notifications.
// For user-initiated responses the control request is already deleted by the
// SendControlResponse handler, but this also covers agent-initiated
// resolutions (e.g. the agent moves on without waiting for user input).
func (a *Agent) handleServerRequestResolved(params json.RawMessage) {
	var notif struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &notif) != nil {
		return
	}
	identity, valid := agent.NewControlRequestIdentity(notif.RequestID)
	if !valid {
		return
	}
	// Codex withdrew the request itself, so it waits for no answer.
	a.WithdrawControlRequest(a.sink, identity.Key)
}

// handleErrorNotification processes error notifications.
func (a *Agent) handleErrorNotification(params json.RawMessage) {
	var notif struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.Message != "" {
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: notif.Message,
		})
	}
}

func discardCompletedCodexGeneration(buffer *providerkit.GenerationBuffer, itemType, itemID string) {
	buffer.Discard(itemID)
	switch itemType {
	case contracts.CodexItemTypeAgentMessage:
		buffer.Discard(codexAssistantFallbackScope)
	case contracts.CodexItemTypePlan:
		buffer.Discard(codexPlanFallbackScope)
	}
}

// persistSharedItemStarted applies item starts that share parent and child behavior.
func persistSharedItemStarted(sink providerkit.ToolSpanServices, event codexItemEvent, agentID string) {
	switch event.itemType {
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView:
		if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: event.params}, event.itemID, event.itemType, false); err != nil {
			slog.Error("codex persist item/started", "agent_id", agentID, "type", event.itemType, "error", err)
		}
	case contracts.CodexItemTypeReasoning:
		if !codexReasoningItemHasText(event.item) {
			return
		}
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.params}, agent.SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType,
		}); err != nil {
			slog.Error("codex persist reasoning/started", "agent_id", agentID, "error", err)
		}
	}
}

// persistSharedItemCompleted applies item completions that need no agent state.
func persistSharedItemCompleted(sink providerkit.ToolLifecycleServices, params json.RawMessage, itemType, itemID, agentID string) {
	sink.ReportProgress(agent.CompleteModelProgress(itemID))
	sink.ReportProgress(agent.CompleteOutputProgress(itemID))
	switch itemType {
	case contracts.CodexItemTypeAgentMessage, contracts.CodexItemTypePlan:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: params}, agent.SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist agentMessage/plan", "agent_id", agentID, "error", err)
		}
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: params}, agent.SpanInfo{
			SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist item/completed", "agent_id", agentID, "type", itemType, "error", err)
		}
		sink.CloseSpan(itemID)
	}
}

// persistCompletedReasoningItem stores the provider's authoritative item when it has visible text.
func (a *Agent) persistCompletedReasoningItem(sink providerkit.GenerationServices, event codexItemEvent, agentID string) {
	sink.ReportProgress(agent.CompleteModelProgress(event.itemID))
	var persistErr error
	if codexReasoningItemHasText(event.item) {
		persistErr = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.params}, agent.SpanInfo{
			SpanID: event.itemID, SpanType: contracts.CodexItemTypeReasoning,
		})
	}
	a.Mu.Lock()
	suffix := "\x00" + event.itemID
	for key := range a.reasoningStreamKind {
		if strings.HasSuffix(key, suffix) {
			delete(a.reasoningStreamKind, key)
			delete(a.reasoningRetainedKind, key)
			delete(a.reasoningSummaryIndex, key)
			delete(a.reasoningSummarySeen, key)
			delete(a.reasoningSummaryBreak, key)
		}
	}
	a.Mu.Unlock()
	if persistErr != nil {
		slog.Error("codex persist reasoning/completed", "agent_id", agentID, "error", persistErr)
		return
	}
}

func codexReasoningItemHasText(item json.RawMessage) bool {
	var reasoning struct {
		Summary []string `json:"summary"`
		Content []string `json:"content"`
		Text    string   `json:"text"`
	}
	if json.Unmarshal(item, &reasoning) != nil {
		return false
	}
	for _, entries := range [][]string{reasoning.Summary, reasoning.Content} {
		for _, entry := range entries {
			if strings.TrimSpace(entry) != "" {
				return true
			}
		}
	}
	return strings.TrimSpace(reasoning.Text) != ""
}

// extractCodexItem extracts the item and routing fields from one item event.
func extractCodexItem(params json.RawMessage) (item json.RawMessage, itemType, itemID, threadID string) {
	var wrapper struct {
		Item     json.RawMessage `json:"item"`
		ThreadID string          `json:"threadId"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("codex extract item wrapper unmarshal failed", "error", err)
		return nil, "", "", ""
	}
	if len(wrapper.Item) == 0 {
		return nil, "", "", ""
	}

	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(wrapper.Item, &header); err != nil {
		slog.Warn("codex extract item header unmarshal failed", "error", err)
		return nil, "", "", ""
	}

	return wrapper.Item, header.Type, header.ID, wrapper.ThreadID
}
