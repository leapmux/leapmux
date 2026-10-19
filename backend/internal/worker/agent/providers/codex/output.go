package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
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

// codexMethodThreadClosed is a Codex method that no contract table lists,
// because only Go reads it.
const codexMethodThreadClosed = "thread/closed"

// handleCodexOutput dispatches one native JSON-RPC frame under outputMu.
func handleCodexOutput(a *Agent, line *providerkit.ParsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	a.handleCodexOutputLocked(line)
}

// handleCodexOutputLocked dispatches live or retained output with outputMu held.
func (a *Agent) handleCodexOutputLocked(line *providerkit.ParsedLine) {
	if a.isRetiredCodexOutput(line.Params) {
		return
	}
	if a.deferOutputAfterCodexSteerEnd(line) {
		return
	}
	slog.Debug("codex HandleOutput", "agent_id", a.AgentID(), "method", line.Method, "len", len(line.Raw))

	switch line.Method {
	case "":
		slog.Debug("codex orphan response", "id", string(line.ID))
		return
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
		// Native exec has no typed mirror. Other raw items retain their existing filtering.
		a.handleRawResponseItemCompleted(line.Params)
		a.handleCodexRawExecution(line.Raw, line.Params)

	case contracts.CodexMethodHookCompleted:
		a.handleHookCompleted(line.Raw, line.Params)

	case contracts.CodexMethodThreadSettingsUpdated:
		a.handleThreadSettingsUpdated(line.Params)

	// Server requests carry an id. Dispatch them by their native method.
	// publishApprovalRequest answers a permission request directly when the bypass preset applies.
	case contracts.MCPElicitationMethodCodex:
		a.publishApprovalRequest(line, providerkit.MCPElicitationCancelAnswer())

	case contracts.CodexMethodCommandExecutionRequestApproval,
		contracts.CodexMethodFileChangeRequestApproval,
		contracts.CodexMethodPermissionsRequestApproval,
		contracts.CodexMethodToolRequestUserInput:
		// Codex retires its own approval requests through serverRequest/resolved, and it
		// defines no outcome for one the client withdraws, so LeapMux sends no answer.
		a.publishApprovalRequest(line, nil)

	case contracts.CodexMethodAutoApprovalReviewStarted:
		// The completed review carries the outcome, and a command review also starts
		// an in-progress commandExecution item, which already shows the command. A
		// started frame on its own would stay in the transcript as a review that never
		// ends. The tool card's badge shows the hold instead.
		a.handleCodexAutoReviewCheck(line, true)

	case contracts.CodexMethodAutoApprovalReviewCompleted:
		a.handleCodexAutoReviewCheck(line, false)
		a.persistCodexAutoReviewFrame(line.Raw, line.Params)

	case contracts.CodexMethodStrictReviewRequired:
		a.persistCodexAutoReviewFrame(line.Raw, line.Params)

	case contracts.CodexMethodGuardianWarning:
		a.handleGuardianWarning(line.Raw, line.Params)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(line.Params)

	case contracts.CodexMethodAccountRateLimitsUpdated:
		a.handleRateLimitsUpdated(line.Raw, line.Params)

	case contracts.CodexMethodWarning:
		a.handleCodexDedupedNotice(line, codexWarningKey(line.Params))

	case contracts.CodexMethodConfigWarning, contracts.CodexMethodDeprecationNotice:
		a.handleCodexDedupedNotice(line, codexNoticeTextKey(line.Method, line.Params))

	case contracts.CodexMethodModelVerification:
		// One fixed notice per flag set per Codex process.
		a.handleCodexDedupedNotice(line, codexVerificationKey(line.Params))

	case contracts.CodexMethodModelSafetyBufferingUpdated:
		a.handleSafetyBufferingUpdated(line)

	case contracts.CodexMethodAuthRecoveryStarted, contracts.CodexMethodAuthRecoveryCompleted:
		a.persistCodexThreadNotification(providerkit.StringField(line.Params, "threadId"), codexPendingChildEvent{kind: codexPendingNotification, raw: line.Raw, params: line.Params})

	case codexMethodThreadClosed:
		a.handleThreadClosed(line.Params)

	// The session goal. These are claimed out of the `default:` case below on
	// purpose: Codex reports the goal after EVERY completed tool call, so the
	// fallback wrote a raw-JSON row per tool call, and each of those rows also
	// broke notification adjacency. The sink keeps the goal as session state and
	// writes the transcript only when the goal actually changes.
	case codexMethodGoalUpdated:
		a.handleGoalUpdated(line.Params)

	case codexMethodGoalCleared:
		a.handleGoalCleared(line.Params)

	case contracts.CodexMethodError:
		a.handleErrorNotification(line)

	default:
		// An inbound REQUEST needs an answer. The comment above states that Codex
		// sends its approval requests with an "id", so one whose method this
		// dispatcher does not recognize reaches here carrying a runtime that waits.
		// A transcript row alone leaves it waiting for its own timeout.
		a.RefuseUnsupportedRequest(line)
		a.persistUnknownCodexNotification(line)
	}
}

// deferOutputAfterCodexSteerEnd preserves main-thread notification order while steer replies remain pending.
// The reader still processes requests and child notifications. A native echo from the held turn confirms delivery immediately.
func (a *Agent) deferOutputAfterCodexSteerEnd(line *providerkit.ParsedLine) bool {
	a.Mu.Lock()
	held := a.pendingSteerTurnEnd != nil
	turnID := a.mainTurn.id
	a.Mu.Unlock()
	if !held || line.HasID() || !a.isMainThreadID(providerkit.StringField(line.Params, "threadId")) {
		return false
	}
	if line.Method == contracts.CodexMethodItemStarted {
		if event, ok := newCodexItemEvent(line.Raw, line.Params); ok && event.itemType == contracts.CodexItemTypeUserMessage &&
			(event.turnID == "" || event.turnID == turnID) && providerkit.StringField(event.item, "clientId") != "" {
			a.handleItemStarted(event.raw, event.params)
			return true
		}
	}
	copy := *line
	copy.Raw = slices.Clone(line.Raw)
	copy.ID = slices.Clone(line.ID)
	copy.Params = slices.Clone(line.Params)
	copy.Result = slices.Clone(line.Result)
	copy.Error = slices.Clone(line.Error)
	a.deferredSteerOutput = append(a.deferredSteerOutput, &copy)
	return true
}

// finishDeferredCodexSteerOutput writes the held turn end after every steer call resolves.
// Call it with outputMu held. Publish the final state after replay.
// This keeps the queue active when a native turn already started.
func (a *Agent) finishDeferredCodexSteerOutput() {
	a.Mu.Lock()
	if a.mainTurn.steers != 0 || a.pendingSteerTurnEnd == nil {
		a.Mu.Unlock()
		return
	}
	end := a.pendingSteerTurnEnd
	a.pendingSteerTurnEnd = nil
	a.Mu.Unlock()
	queued := a.deferredSteerOutput
	a.deferredSteerOutput = nil
	a.replayingSteerOutput = true
	a.handleTurnCompleted(end)
	for _, line := range queued {
		a.handleCodexOutputLocked(line)
	}
	a.replayingSteerOutput = false
	a.PublishTurnActive()
}

// persistUnknownCodexNotification stores one raw frame the dispatcher does not
// claim. The frame keeps every field a future reader needs, and its `threadId`
// selects the transcript that owns it: a child's frame must never reach the
// parent transcript, whose to-do list reads only the parent's own rows. Each
// frame stores as its own message row, so the plan card reads the frame the row
// holds.
func (a *Agent) persistUnknownCodexNotification(line *providerkit.ParsedLine) {
	a.persistCodexThreadNotification(providerkit.StringField(line.Params, "threadId"), codexPendingChildEvent{kind: codexPendingRawMessage, raw: line.Raw, params: line.Params})
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
	if a.isMainThreadID(notif.ThreadID) {
		a.handleMainTurnStarted(notif.Turn.ID)
		return
	}
	a.clearReasoningStateForThread(notif.ThreadID)
	a.clearInterruptCallsForThread(notif.ThreadID)
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
	if a.mainTurn.id == turnID && a.mainTurn.ended != nil {
		a.Mu.Unlock()
		return
	}
	previousTurnID := a.mainTurn.id
	withdrawOffer := a.retryOffer != nil && a.retryOffer.turnID != turnID
	a.Mu.Unlock()
	if previousTurnID != "" && previousTurnID != turnID {
		a.persistOpenCodexCompactionEnd(a.currentThreadID(), agent.MessageCompletionInterrupted)
		a.flushCodexGeneration(agent.MessageCompletionInterrupted)
		a.persistIncompleteCodexTools("", false, agent.MessageCompletionInterrupted)
	}
	threadID := a.currentThreadID()
	a.clearReasoningStateForThread(threadID)
	a.clearInterruptCallsForThread(threadID)
	a.clearCodexRawExecutionsBeforeTurn(threadID, turnID)
	a.Mu.Lock()
	a.mainTurn.finish()
	a.mainTurn = codexMainTurn{id: turnID, ended: make(chan struct{})}
	pending := a.pendingStart
	matched := pending != nil && pending.threadID == threadID
	publishSettings := false
	if matched {
		a.mainTurn.input = pending.input
		if pending.retry != nil {
			if !pending.modelObserved && a.codexStartOwnsSettingLocked(pending, agent.OptionIDModel) {
				a.model = pending.settings.model
			}
			if !pending.effortObserved && a.codexStartOwnsSettingLocked(pending, agent.OptionIDEffort) {
				a.effort = pending.settings.effort
			}
			publishSettings = true
		}
		if a.permissionSettingsLocked() == pending.settings.permissions {
			a.permissionsAcknowledged = true
		}
		if pending.cancelled || pending.retry != nil && pending.retry.cancelled {
			pending.cancelDone = make(chan error, 1)
			a.mainTurn.input = nil
		}
		a.finishCodexTurnStartLocked(pending, turnID, nil)
	}
	a.TurnToolUses = 0
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.Mu.Unlock()
	if withdrawOffer {
		a.withdrawTurnRetryOffer()
	}
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetModelProgress())
	if matched {
		if publishSettings {
			a.publishSettings()
		}
		a.approveOutstandingForBypass()
		if pending.cancelDone != nil {
			go func() { pending.cancelDone <- a.interruptCancelledCodexStart(threadID, turnID) }()
		}
	}
}

func (a *Agent) handleChildTurnStarted(threadID, turnID string, route codexChildRoute) {
	a.clearCodexRawExecutionsBeforeTurn(threadID, turnID)
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
	turnID   string
}

func newCodexItemEvent(raw []byte, params json.RawMessage) (codexItemEvent, bool) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return codexItemEvent{}, false
	}
	var envelope struct {
		TurnID string `json:"turnId"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		return codexItemEvent{}, false
	}
	return codexItemEvent{
		raw:      raw,
		params:   params,
		item:     item,
		itemType: itemType,
		itemID:   itemID,
		threadID: threadID,
		turnID:   envelope.TurnID,
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
	switch event.itemType {
	case contracts.CodexItemTypeAgentMessage, contracts.CodexItemTypeReasoning:
		a.discardFailedCodexAttempt(event.threadID, func(scopeID string, kind agent.AssembledMessageKind) bool {
			return scopeID != event.itemID && (kind == agent.AssembledMessageKindText || kind == agent.AssembledMessageKindReasoning)
		})
	case contracts.CodexItemTypePlan:
		a.discardFailedCodexAttempt(event.threadID, func(scopeID string, _ agent.AssembledMessageKind) bool {
			return scopeID == event.itemID
		})
	}
	if event.itemType == contracts.CodexItemTypeUserMessage {
		a.persistDroppedCodexResponse(event.threadID)
		if a.isMainThreadID(event.threadID) {
			a.steerLedger.EchoKey(providerkit.StringField(event.item, "clientId"))
			a.steerLedger.Settle(a.sink)
		}
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
	if event.itemType != contracts.CodexItemTypeUserMessage {
		a.codexRetryResolution(mainThread, event.threadID).Resolve(sink)
	}
	if codexItemIsTool(event.itemType) {
		ownerThreadID := event.threadID
		if mainThread {
			ownerThreadID = ""
		}
		a.rememberCodexIncompleteTool(event.itemID, event.itemType, ownerThreadID, event.params)
	}
	switch event.itemType {
	case contracts.CodexItemTypeAgentMessage:
		if mainThread {
			// The first agent text ends any retry offer: from here the faster
			// model would answer a different request.
			a.markCodexTurnAgentMessage()
		}
		// Wait for the authoritative completed item.
	case contracts.CodexItemTypeContextCompaction:
		a.Mu.Lock()
		threadID := event.threadID
		if mainThread {
			threadID = a.threadID
		}
		if a.openCompactions == nil {
			a.openCompactions = make(map[string]codexOpenCompaction)
		}
		a.openCompactions[threadID] = codexOpenCompaction{itemID: event.itemID, turnID: event.turnID}
		a.Mu.Unlock()
		providerkit.PublishCompaction(sink, "")
		if mainThread {
			a.Mu.Lock()
			if a.compactionStartAck != nil {
				close(a.compactionStartAck)
				a.compactionStartAck = nil
			}
			a.Mu.Unlock()
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
	if event.itemType != contracts.CodexItemTypeUserMessage {
		a.codexRetryResolution(mainThread, event.threadID).Resolve(sink)
	}
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
	case contracts.CodexItemTypeUserMessage:
		// The queue row and the steer ledger already state this input.
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
		if persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID) {
			a.observeCodexRawExecutionTool(event)
		}
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
		a.Mu.Lock()
		threadID := event.threadID
		if mainThread {
			threadID = a.threadID
		}
		open, present := a.openCompactions[threadID]
		matches := !present || open.itemID == event.itemID && (open.turnID == "" || event.turnID == "" || open.turnID == event.turnID)
		if matches {
			delete(a.openCompactions, threadID)
		}
		a.Mu.Unlock()
		if matches {
			providerkit.EndCompaction(sink)
		}
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.raw}); err != nil {
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
	a.Mu.Lock()
	if a.mainTurn.steers != 0 {
		a.pendingSteerTurnEnd = slices.Clone(params)
		a.Mu.Unlock()
		a.withdrawTurnRetryOffer()
		a.PublishTurnActive()
		return
	}
	a.Mu.Unlock()

	a.retryResolution.Reset()
	completion := codexTurnCompletion(params)
	a.persistOpenCodexCompactionEnd(notif.ThreadID, completion)
	a.flushCodexGeneration(completion)
	incompleteToolUses := a.persistIncompleteCodexTools("", false, completion)
	a.finishCodexRawExecutions(notif.ThreadID, completion)
	if completion == agent.MessageCompletionInterrupted {
		a.steerLedger.DropAll()
	} else {
		a.steerLedger.EchoOldest(len(a.steerLedger.Pending()))
	}
	a.steerLedger.Settle(a.sink)

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
	a.mainTurn.finish()
	a.turnSawPlan = false
	a.turnPlanText = ""
	clear(a.codexSpawnPrompts)
	a.Mu.Unlock()
	// The turn ended, so its retry offer ends with it.
	a.withdrawTurnRetryOffer()
	// Publish after PersistTurnEnd records the tool count. An earlier publish could use an empty count for the completion sound.
	// A deferred end can run on the steer caller. Both paths hold outputMu.
	// The input queue holds no coordinator lock across provider calls, so this synchronous publish is safe.
	// During replay, publish only the final state to keep a later native turn active.
	defer func() {
		if !a.replayingSteerOutput {
			a.PublishTurnActive()
		}
	}()
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
	a.codexRetryResolution(false, threadID).Reset()
	completion := codexTurnCompletion(params)
	a.persistOpenCodexCompactionEnd(threadID, completion)
	a.flushCodexChildGeneration(threadID, completion)
	a.persistIncompleteCodexTools(threadID, false, completion)
	a.finishCodexRawExecutions(threadID, completion)
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
			a.sink.UpdateBackgroundTaskStatus(threadID, transition.status, transition.activity))
	}
}

// persistOpenCodexCompactionEnd ends a compaction for which Codex sent no completed item.
func (a *Agent) persistOpenCodexCompactionEnd(threadID string, completion agent.MessageCompletion) {
	a.Mu.Lock()
	if threadID == "" {
		threadID = a.threadID
	}
	_, present := a.openCompactions[threadID]
	delete(a.openCompactions, threadID)
	a.Mu.Unlock()
	if !present {
		return
	}
	sink, routed := a.codexReadyThreadSink(threadID)
	if !routed {
		return
	}
	providerkit.EndCompaction(sink)
	outcome := providerkit.CompactionEndFailed
	if completion == agent.MessageCompletionInterrupted {
		outcome = providerkit.CompactionEndAborted
	}
	providerkit.PersistCompactionEnded(sink, outcome, "")
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
	if err := providerkit.FlushGeneration(buffer, sink, completion, a.IsDiscardingOutput()); err != nil {
		slog.Error("persist codex generation", "agent_id", a.AgentID(), "error", err)
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
	a.persistCodexThreadNotification(notification.ThreadID, codexPendingChildEvent{kind: codexPendingHookCompleted, raw: content, params: params})
}

// codexAutoReviewDenialLimitPrefix opens the one guardianWarning that the transcript
// keeps. Codex emits a guardianWarning for EVERY finished review, which repeats what
// the completed review states, and once more when it interrupts a turn after too many
// denials (ext/guardian-reviewer/src/reporting.rs, record_denial). Only that second
// warning explains why the turn stopped. The warning has no kind field, so its text
// identifies it, as the Codex TUI and the ChatGPT app also identify it.
const codexAutoReviewDenialLimitPrefix = "Automatic approval review rejected too many approval requests for this turn"

// handleGuardianWarning keeps the denial-limit warning and drops the copy that each
// finished review emits.
func (a *Agent) handleGuardianWarning(content []byte, params json.RawMessage) {
	var warning struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &warning) != nil || !strings.HasPrefix(warning.Message, codexAutoReviewDenialLimitPrefix) {
		return
	}
	a.persistCodexAutoReviewFrame(content, params)
}

// persistCodexAutoReviewFrame keeps one auto-review frame in the transcript of the
// thread that it belongs to.
func (a *Agent) persistCodexAutoReviewFrame(content []byte, params json.RawMessage) {
	var frame struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &frame)
	a.persistCodexThreadNotification(frame.ThreadID, codexPendingChildEvent{kind: codexPendingAutoReview, raw: content, params: params})
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
	a.persistCodexThreadNotification(notification.ThreadID, codexPendingChildEvent{kind: codexPendingMcpOauthCompleted, raw: content, params: params})
}

// handleMcpStartupStatusUpdated keeps only failures and unknown future states.
func (a *Agent) handleMcpStartupStatusUpdated(content []byte, params json.RawMessage) {
	switch codexMcpStartupState(params) {
	case "starting", "ready", "cancelled":
		return
	}
	a.persistCodexThreadNotification(providerkit.StringField(params, "threadId"), codexPendingChildEvent{kind: codexPendingMcpStartupUpdated, raw: content, params: params})
}

// persistCodexThreadNotification stores one raw frame in the transcript that
// owns its thread. A child frame waits in the child queue until the route
// exists. The kind states the persist primitive, so a queued frame renders the
// same way as one the route resolved at once.
func (a *Agent) persistCodexThreadNotification(threadID string, event codexPendingChildEvent) {
	if a.isMainThreadID(threadID) {
		a.persistCodexNotificationForSink(a.sink, a.AgentID(), event)
		return
	}
	route, routed := a.ensureCodexChildRoute(threadID)
	if !routed {
		event.raw = append(json.RawMessage(nil), event.raw...)
		event.params = append(json.RawMessage(nil), event.params...)
		a.enqueuePendingCodexChildEvent(threadID, event)
		return
	}
	a.replayPendingCodexChildEvents(threadID, route)
	a.persistCodexNotificationForSink(route.childSink, route.agentID, event)
}

// persistCodexNotificationForSink stores one routed raw frame for one sink. A
// frame the switch claims stores as a notification-thread entry: hook failures,
// MCP failures, safety-buffering notices and the RENDER notices. A frame the
// dispatcher does not claim stores as its own message row, so the plan card
// reads the frame the row holds.
func (a *Agent) persistCodexNotificationForSink(sink agent.ProviderServices, agentID string, event codexPendingChildEvent) {
	var err error
	if event.kind == codexPendingRawMessage {
		err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.raw}, agent.SpanInfo{})
	} else {
		_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.raw})
	}
	if err != nil {
		slog.Error("Cannot persist the Codex thread event", "agent_id", agentID, "kind", event.kind, "error", err)
		if !agent.OutputRetained(err) {
			return
		}
	}
	if event.retryNotice {
		threadID := providerkit.StringField(event.params, "threadId")
		a.codexRetryResolution(a.isMainThreadID(threadID), threadID).NoteRetry()
	}
	if event.noticeKey != "" {
		a.Mu.Lock()
		if a.acceptedNoticeKeys == nil {
			a.acceptedNoticeKeys = make(map[string]struct{})
		}
		a.acceptedNoticeKeys[event.noticeKey] = struct{}{}
		a.Mu.Unlock()
	}
}

func (a *Agent) handleRawResponseItemCompleted(params json.RawMessage) {
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
		notification.Item.Type != codexRawItemFunctionCall || notification.Item.Name != "spawn_agent" ||
		notification.Item.Namespace != codexMultiAgentV2Namespace || notification.Item.CallID == "" {
		return
	}
	var arguments struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(notification.Item.Arguments), &arguments) != nil {
		return
	}
	a.rememberCodexSpawnPrompt(notification.Item.CallID, arguments.Message)
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
	// Codex also emits this event after a client answer removes the registry record.
	// The service owns finalization of that answer. Cancel only a request that still waits here.
	a.WithdrawOutstandingControlRequest(a.sink, identity.Key)
}

// handleErrorNotification processes error notifications.
//
// Codex sends error while it retries and after it stops.
// A willRetry frame discards open scopes before its notice enters the owning transcript.
// The first model item after the notice resolves that retry through the shared worker notification.
// Drop a final error frame. The failed turn's turn/completed frame already carries the same error and details.
func (a *Agent) handleErrorNotification(line *providerkit.ParsedLine) {
	var notif struct {
		Error struct {
			Message           string          `json:"message"`
			CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
			AdditionalDetails *string         `json:"additionalDetails"`
		} `json:"error"`
		WillRetry bool   `json:"willRetry"`
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
	}
	if json.Unmarshal(line.Params, &notif) != nil || !notif.WillRetry {
		return
	}
	a.discardFailedCodexAttempt(notif.ThreadID, nil)
	a.persistCodexThreadNotification(notif.ThreadID, codexPendingChildEvent{kind: codexPendingNotification, raw: line.Raw, params: line.Params, retryNotice: true})
}

// codexRetryResolution returns the retry state for one transcript. Call it with outputMu held.
func (a *Agent) codexRetryResolution(mainThread bool, threadID string) *providerkit.RetryResolution {
	if mainThread {
		return &a.retryResolution
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return &a.codexChildStateLocked(threadID).retryResolution
}

// codexNoticeTextKey builds the dedupe key of one repeating notice. It joins
// the method and the fields that the display text reads, so two notices of one
// method with the same text share one row. The method keeps two methods apart:
// a config warning and a deprecation notice with the same fields draw different
// text.
func codexNoticeTextKey(method string, params json.RawMessage) string {
	var notif struct {
		Summary string `json:"summary"`
		Details string `json:"details"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal(params, &notif)
	return codexNoticeKey([]string{method, notif.Summary, notif.Details, notif.Path})
}

// codexWarningKey keeps each thread's warning text separate from other threads and notices.
func codexWarningKey(params json.RawMessage) string {
	var warning struct {
		ThreadID string `json:"threadId"`
		Message  string `json:"message"`
		Warning  struct {
			Message string `json:"message"`
		} `json:"warning"`
	}
	_ = json.Unmarshal(params, &warning)
	return codexNoticeKey([]string{contracts.CodexMethodWarning, warning.ThreadID, warning.Message, warning.Warning.Message})
}

// codexVerificationKey builds the dedupe key of one verification notice. The
// frontend draws text only for some flags, so the key holds the sorted flag
// set: a frame without a drawn flag must not use up the row of a frame that
// has one.
func codexVerificationKey(params json.RawMessage) string {
	var notif struct {
		Verifications []string `json:"verifications"`
	}
	_ = json.Unmarshal(params, &notif)
	flags := slices.Clone(notif.Verifications)
	slices.Sort(flags)
	return codexNoticeKey(append([]string{contracts.CodexMethodModelVerification}, flags...))
}

// codexNoticeKey preserves field boundaries, including strings that contain NUL.
func codexNoticeKey(fields []string) string {
	// A slice of strings contains no value that JSON cannot encode.
	encoded, _ := json.Marshal(fields)
	return string(encoded)
}

// handleCodexDedupedNotice persists one notice per distinct text per Codex
// process. A notice that repeats on every call tells the reader nothing new.
func (a *Agent) handleCodexDedupedNotice(line *providerkit.ParsedLine, key string) {
	a.Mu.Lock()
	if _, stored := a.acceptedNoticeKeys[key]; stored {
		a.Mu.Unlock()
		if sink, ready := a.codexReadyThreadSink(providerkit.StringField(line.Params, "threadId")); ready {
			if err := agent.FlushPendingOutput(sink); err != nil {
				slog.Error("retry retained Codex notice", "error", err)
			}
		}
		return
	}
	// Pending events are their own reservations. A rejected enqueue or a removed
	// child leaves no separate reservation that could suppress a later notice.
	for _, child := range a.collabChildren {
		for _, event := range child.pendingEvents {
			if event.noticeKey == key {
				a.Mu.Unlock()
				return
			}
		}
	}
	a.Mu.Unlock()
	a.persistCodexThreadNotification(providerkit.StringField(line.Params, "threadId"), codexPendingChildEvent{
		kind: codexPendingNotification, raw: line.Raw, params: line.Params, noticeKey: key,
	})
}

// handleCodexAutoReviewCheck updates the hold for the native review target.
// Every completed outcome ends the hold. Its dispatch case persists the outcome.
func (a *Agent) handleCodexAutoReviewCheck(line *providerkit.ParsedLine, checking bool) {
	var notif struct {
		ThreadID     string `json:"threadId"`
		TargetItemID string `json:"targetItemId"`
	}
	_ = json.Unmarshal(line.Params, &notif)
	a.publishCodexPermissionCheck(notif.ThreadID, notif.TargetItemID, checking)
}

// publishCodexPermissionCheck reports the automatic review that holds one call.
// A child thread's review broadcasts on the child sink, and an unrouted child
// broadcasts nothing.
func (a *Agent) publishCodexPermissionCheck(threadID, targetItemID string, checking bool) {
	if targetItemID == "" {
		return
	}
	sink, routed := a.codexReadyThreadSink(threadID)
	if !routed {
		return
	}
	providerkit.PublishPermissionCheck(sink, targetItemID, checking)
}

// handleThreadClosed drops the notice. A main thread that closes under the
// running worker gets one warning log.
func (a *Agent) handleThreadClosed(params json.RawMessage) {
	threadID := providerkit.StringField(params, "threadId")
	if threadID != "" && a.isMainThreadID(threadID) {
		slog.Warn("codex closed the main thread", "agent_id", a.AgentID(), "thread", threadID)
	}
}

// codexTurnRetryLearnMoreURL is the Codex help page for the extra safety
// buffering that makes a turn retry available.
const codexTurnRetryLearnMoreURL = "https://help.openai.com/en/articles/20001326"

// broadcastTurnRetryOffer broadcasts the live turn-retry offer, or its
// withdrawal for a nil offer. Call it with a.retryOfferMu held, right after the
// critical section that changed a.retryOffer, so the broadcasts keep the order
// of the changes.
func (a *Agent) broadcastTurnRetryOffer(offer *codexTurnRetryOffer) {
	// A NIL sink publishes nowhere, as providerkit.PublishTurnStateTo states.
	if a.sink == nil {
		return
	}
	value := interface{}(nil)
	if offer != nil {
		value = map[string]interface{}{
			contracts.TurnRetryOfferFieldTurnId:       offer.turnID,
			contracts.TurnRetryOfferFieldModel:        offer.model,
			contracts.TurnRetryOfferFieldLearnMoreUrl: codexTurnRetryLearnMoreURL,
		}
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyTurnRetryOffer: value,
	})
}

// withdrawTurnRetryOffer ends the live turn-retry offer and broadcasts the
// withdrawal.
func (a *Agent) withdrawTurnRetryOffer() {
	a.retryOfferMu.Lock()
	defer a.retryOfferMu.Unlock()
	a.Mu.Lock()
	a.retryOffer = nil
	a.Mu.Unlock()
	a.broadcastTurnRetryOffer(nil)
}

// markCodexTurnAgentMessage records that the running turn produced its first
// agent text. The turn's retry offer ends here: from this point the faster model
// would answer a different request.
func (a *Agent) markCodexTurnAgentMessage() {
	a.Mu.Lock()
	turnID := a.mainTurn.id
	if turnID == "" || a.mainTurn.agentMessageStarted {
		a.Mu.Unlock()
		return
	}
	a.mainTurn.agentMessageStarted = true
	a.mainTurn.input = nil
	a.Mu.Unlock()
	a.withdrawTurnRetryOffer()
}

// handleSafetyBufferingUpdated shows the buffering notice and offers a retry on
// a faster model while the request still buffers. A child thread persists its
// own notice and holds no offer: the offer belongs to the running main turn.
func (a *Agent) handleSafetyBufferingUpdated(line *providerkit.ParsedLine) {
	var notif struct {
		ThreadID        string `json:"threadId"`
		TurnID          string `json:"turnId"`
		ShowBufferingUI bool   `json:"showBufferingUi"`
		FasterModel     string `json:"fasterModel"`
	}
	_ = json.Unmarshal(line.Params, &notif)
	if notif.ShowBufferingUI {
		a.persistCodexThreadNotification(notif.ThreadID, codexPendingChildEvent{kind: codexPendingNotification, raw: line.Raw, params: line.Params})
	}
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}
	a.retryOfferMu.Lock()
	defer a.retryOfferMu.Unlock()
	// The check and the record share one critical section. A retry that starts
	// between them would otherwise find no offer to spend, and this notice would
	// then record an offer that the running retry makes stale.
	a.Mu.Lock()
	if notif.TurnID == "" || notif.TurnID != a.mainTurn.id {
		a.Mu.Unlock()
		return
	}
	// The Codex TUI offers the retry only while the answer can still be replayed:
	// a faster model, the running turn, no agent text yet, the turn's input
	// retained, and no retry in flight.
	eligible := a.turnRetrySupported && notif.ShowBufferingUI && notif.FasterModel != "" && notif.FasterModel != a.model &&
		a.turnInputRetainedLocked(notif.TurnID) && !a.mainTurn.agentMessageStarted &&
		a.retry == nil && a.pendingStart == nil
	var offer *codexTurnRetryOffer
	if eligible {
		offer = &codexTurnRetryOffer{turnID: notif.TurnID, model: notif.FasterModel}
	}
	previous := a.retryOffer
	a.retryOffer = offer
	a.Mu.Unlock()
	if offer != nil || previous != nil {
		a.broadcastTurnRetryOffer(offer)
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
func persistSharedItemCompleted(sink providerkit.ToolLifecycleServices, params json.RawMessage, itemType, itemID, agentID string) bool {
	sink.ReportProgress(agent.CompleteModelProgress(itemID))
	sink.ReportProgress(agent.CompleteOutputProgress(itemID))
	stored := false
	switch itemType {
	case contracts.CodexItemTypeAgentMessage, contracts.CodexItemTypePlan:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: params}, agent.SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist agentMessage/plan", "agent_id", agentID, "error", err)
		} else {
			stored = true
		}
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: params}, agent.SpanInfo{
			SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist item/completed", "agent_id", agentID, "type", itemType, "error", err)
		} else {
			stored = true
		}
		sink.CloseSpan(itemID)
	}
	return stored
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
			a.deleteCodexReasoningStateLocked(key)
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

func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	handleCodexOutput(a, line)
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *Agent) HandleOutput(content []byte) {
	handleCodexOutput(a, providerkit.ParseLine(content))
}
