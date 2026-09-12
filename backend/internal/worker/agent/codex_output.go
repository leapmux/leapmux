package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var codexRetryableDisconnectPattern = regexp.MustCompile(`^stream disconnected before completion(?:$|[^[:alnum:]].*)`)

// codexSystemMetadataMethods are Codex-emitted JSON-RPC notifications that
// carry agent/system metadata (auto-compaction, lifecycle, MCP startup, skills
// invalidation, remote-control status). They share one handler — persist
// verbatim as agent-emitted notifications. Methods with extra side effects
// (rate-limit, token-usage broadcasts) keep dedicated cases below. A method
// that this table omits falls to the default branch and lands in the transcript
// as a raw JSON-RPC bubble.
var codexSystemMetadataMethods = map[string]struct{}{
	"thread/compacted":                {},
	"thread/name/updated":             {},
	"skills/changed":                  {},
	"remoteControl/status/changed":    {},
	"mcpServer/startupStatus/updated": {},
}

// handleCodexOutput processes a single parsed JSONL notification from the Codex app-server.
// Codex messages are stored in their native JSON-RPC format.
func handleCodexOutput(a *CodexAgent, line *parsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.isRetiredCodexOutput(line.Params) {
		return
	}
	slog.Debug("codex HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))

	if _, ok := codexSystemMetadataMethods[line.Method]; ok {
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw); err != nil {
			slog.Error("codex persist system metadata", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
		return
	}

	switch line.Method {
	case "turn/started":
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

	case "item/started":
		a.handleItemStarted(line.Raw, line.Params)

	case "item/completed":
		a.handleItemCompleted(line.Raw, line.Params)

	case "turn/completed":
		a.handleTurnCompleted(line.Params)

	case "thread/tokenUsage/updated":
		a.handleTokenUsageUpdated(line.Raw, line.Params)

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case contracts.MCPElicitationMethodCodex,
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"item/tool/requestUserInput":
		a.handleApprovalRequest(line.IDString(), line.Raw)

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
		// Persist unknown notifications so the frontend can decide how to render them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: line.Raw}, SpanInfo{}); err != nil {
			slog.Error("codex persist notification", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
	}
}

// handleTurnStarted processes turn/started notifications.
//
// Resets per-turn state and broadcasts the new turn ID so the frontend
// can wire up interrupt. Git status is refreshed automatically at
// turn-end by the sink layer.
func (a *CodexAgent) handleTurnStarted(params json.RawMessage) {
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

func (a *CodexAgent) handleMainTurnStarted(turnID string) {
	a.mu.Lock()
	if a.turnStartAck != nil {
		close(a.turnStartAck)
		a.turnStartAck = nil
	}
	a.turnID = turnID
	a.turnToolUses = 0
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.mu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(ResetModelProgress())
}

func (a *CodexAgent) handleChildTurnStarted(threadID, turnID string, route codexChildRoute) {
	a.activateCollabChild(threadID)
	a.setChildTurnID(threadID, turnID)
	a.flushCodexChildGeneration(threadID, MessageCompletionInterrupted)
	route.childSink.ReportProgress(ResetModelProgress())
	_, _, err := a.upsertCodexChildRegistryRow(threadID, codexChildTransition{
		status:   bgtask.StatusRunning,
		activity: "working",
	})
	logRegistryRefusal("codex", "upsert", err)
	a.publishCodexChildTurnActive(route.childSink, true)
}

func (a *CodexAgent) clearReasoningStateForThread(threadID string) {
	prefix := threadID + "\x00"
	a.mu.Lock()
	for key := range a.reasoningStreamKind {
		if strings.HasPrefix(key, prefix) {
			delete(a.reasoningStreamKind, key)
			delete(a.reasoningRetainedKind, key)
			delete(a.reasoningSummaryIndex, key)
			delete(a.reasoningSummarySeen, key)
			delete(a.reasoningSummaryBreak, key)
		}
	}
	a.mu.Unlock()
}

// bufferCodexModelText routes one model delta to its counter and buffer.
func (a *CodexAgent) bufferCodexModelText(
	scopeID, threadID, text string,
	kind AssembledMessageKind,
	join textJoin,
	reportProgress bool,
) {
	sink := a.sink
	buffer := &a.generationBuffer
	if !a.isMainThreadID(threadID) {
		route, routed := a.ensureCodexChildRoute(threadID)
		buffer = a.codexChildGenerationBuffer(threadID)
		if !routed {
			a.appendPendingCodexChildGeneration(threadID, scopeID, kind, text, join)
			return
		}
		a.replayPendingCodexChildEvents(threadID, route)
		a.clearPendingCodexChildGenerationBytes(threadID)
		sink = route.childSink
	}
	if reportProgress {
		sink.ReportProgress(ModelTextProgress(scopeID, text))
	}
	buffer.Append(scopeID, kind, text, join)
}

func (a *CodexAgent) reportCodexModelProgress(scopeID, threadID, text string) {
	sink := a.sink
	if !a.isMainThreadID(threadID) {
		route, routed := a.ensureCodexChildRoute(threadID)
		if !routed {
			return
		}
		a.replayPendingCodexChildEvents(threadID, route)
		sink = route.childSink
	}
	sink.ReportProgress(ModelTextProgress(scopeID, text))
}

func (a *CodexAgent) discardCodexModelText(scopeID, threadID string) {
	if a.isMainThreadID(threadID) {
		a.generationBuffer.Discard(scopeID)
		return
	}
	a.codexChildGenerationBuffer(threadID).Discard(scopeID)
}

func (a *CodexAgent) codexChildGenerationBuffer(threadID string) *GenerationBuffer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &a.codexChildStateLocked(threadID).generationBuffer
}

func (a *CodexAgent) appendPendingCodexChildGeneration(
	threadID, scopeID string,
	kind AssembledMessageKind,
	text string,
	join textJoin,
) {
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	remaining := codexPendingChildGenerationLimit - state.pendingGenerationBytes
	if remaining < len(text) {
		firstDrop := !state.pendingOutputDropped
		state.pendingOutputDropped = true
		a.mu.Unlock()
		if firstDrop {
			slog.Warn("codex pending child generation limit reached", "thread", threadID)
		}
		return
	}
	state.pendingGenerationBytes += len(text)
	buffer := &state.generationBuffer
	a.mu.Unlock()
	buffer.Append(scopeID, kind, text, join)
}

func (a *CodexAgent) clearPendingCodexChildGenerationBytes(threadID string) {
	a.mu.Lock()
	if state := a.collabChildren[threadID]; state != nil {
		state.pendingGenerationBytes = 0
	}
	a.mu.Unlock()
}

// childSinkForItem resolves the child services that own a tool item. Output
// deltas carry only an item ID, so collabChildItems restores the thread route.
func (a *CodexAgent) childSinkForItem(itemID string) (ProviderServices, bool) {
	if itemID == "" {
		return nil, false
	}
	a.mu.Lock()
	threadID := a.collabChildItems[itemID]
	a.mu.Unlock()
	if threadID == "" {
		return nil, false
	}
	route, routed := a.ensureCodexChildRoute(threadID)
	if !routed {
		return nil, true
	}
	a.replayPendingCodexChildEvents(threadID, route)
	return route.childSink, true
}

// Codex reasoning sub-stream kinds. A single reasoning item can surface as a
// condensed summary stream and/or the raw reasoning stream, both under one
// itemId; observeReasoningText counts only the first-seen kind per item.
const (
	codexReasoningKindSummary   = "summary"
	codexReasoningKindRaw       = "raw"
	codexAssistantFallbackScope = "codex:assistant"
	codexPlanFallbackScope      = "codex:plan"
	codexIncompleteOutputLimit  = 8 << 20
)

type codexToolOutputEvent struct {
	text string
}

type codexIncompleteTool struct {
	params          json.RawMessage
	itemType        string
	childThreadID   string
	outputEvents    []codexToolOutputEvent
	outputBytes     int
	outputTruncated bool
	order           uint64
}

type codexIncompleteToolSnapshot struct {
	params          json.RawMessage
	itemType        string
	childThreadID   string
	output          string
	outputTruncated bool
	order           uint64
}

// observeReasoningText feeds a reasoning delta into the token counter,
// counting only the FIRST reasoning sub-stream ("summary" or "raw") seen for a
// given reasoning itemId. Codex can stream both summaryTextDelta and textDelta
// for the SAME item -- the same generation surfaced two ways -- so counting both
// would roughly double the estimate. Locking onto whichever kind arrives first
// avoids the double count while still moving the counter for models that stream
// only one kind.
func (a *CodexAgent) observeReasoningText(itemID, kind, threadID, text string, summaryIndex *int) {
	key := codexReasoningKey(threadID, itemID)
	a.mu.Lock()
	if a.reasoningStreamKind == nil {
		a.reasoningStreamKind = make(map[string]string)
		a.reasoningRetainedKind = make(map[string]string)
		a.reasoningSummaryIndex = make(map[string]int)
		a.reasoningSummarySeen = make(map[string]bool)
		a.reasoningSummaryBreak = make(map[string]bool)
	}
	countKind := a.reasoningStreamKind[key]
	if countKind == "" {
		countKind = kind
		a.reasoningStreamKind[key] = kind
	}
	retainedKind := a.reasoningRetainedKind[key]
	resetRetained := kind == codexReasoningKindSummary && retainedKind == codexReasoningKindRaw
	retainText := retainedKind == "" || retainedKind == kind || resetRetained
	if kind == codexReasoningKindRaw && retainedKind == codexReasoningKindSummary {
		retainText = false
	}
	if retainText {
		a.reasoningRetainedKind[key] = kind
	}
	join := joinVerbatim
	if kind == codexReasoningKindSummary && retainText {
		// summaryTextDelta is a verbatim delta inside one summary part.
		// summaryPartAdded and a changed summaryIndex start a new paragraph.
		if a.reasoningSummarySeen[key] && (a.reasoningSummaryBreak[key] ||
			summaryIndex != nil && *summaryIndex != a.reasoningSummaryIndex[key]) {
			join = joinParagraph
		}
		if summaryIndex != nil {
			a.reasoningSummaryIndex[key] = *summaryIndex
		}
		a.reasoningSummarySeen[key] = true
		a.reasoningSummaryBreak[key] = false
	}
	a.mu.Unlock()

	if resetRetained {
		a.discardCodexModelText(itemID, threadID)
	}
	if retainText {
		a.bufferCodexModelText(itemID, threadID, text, AssembledMessageKindReasoning, join, countKind == kind)
	} else if countKind == kind {
		a.reportCodexModelProgress(itemID, threadID, text)
	}
}

func codexReasoningKey(threadID, itemID string) string {
	return threadID + "\x00" + itemID
}

type codexModelDelta struct {
	ItemID       string `json:"itemId"`
	Delta        string `json:"delta"`
	ThreadID     string `json:"threadId"`
	SummaryIndex *int   `json:"summaryIndex"`
}

func parseCodexModelDelta(params json.RawMessage) (codexModelDelta, bool) {
	var delta codexModelDelta
	if json.Unmarshal(params, &delta) != nil || delta.Delta == "" {
		return codexModelDelta{}, false
	}
	return delta, true
}

func (a *CodexAgent) markReasoningSummaryBreak(itemID, threadID string, summaryIndex *int) {
	key := codexReasoningKey(threadID, itemID)
	a.mu.Lock()
	if a.reasoningSummaryBreak == nil {
		a.reasoningSummaryBreak = make(map[string]bool)
	}
	if a.reasoningSummaryIndex == nil {
		a.reasoningSummaryIndex = make(map[string]int)
	}
	if summaryIndex == nil {
		a.reasoningSummaryBreak[key] = a.reasoningSummarySeen[key]
	} else {
		if a.reasoningSummarySeen[key] && *summaryIndex != a.reasoningSummaryIndex[key] {
			a.reasoningSummaryBreak[key] = true
		}
		a.reasoningSummaryIndex[key] = *summaryIndex
	}
	a.mu.Unlock()
}

// handleAgentMessageDelta counts and buffers item/agentMessage/delta.
func (a *CodexAgent) handleAgentMessageDelta(params json.RawMessage) {
	if delta, ok := parseCodexModelDelta(params); ok {
		if delta.ItemID == "" {
			delta.ItemID = codexAssistantFallbackScope
		}
		a.bufferCodexModelText(delta.ItemID, delta.ThreadID, delta.Delta, AssembledMessageKindText, joinVerbatim, true)
	}
}

// handlePlanDelta counts and buffers item/plan/delta.
func (a *CodexAgent) handlePlanDelta(params json.RawMessage) {
	if delta, ok := parseCodexModelDelta(params); ok {
		if delta.ItemID == "" {
			delta.ItemID = codexPlanFallbackScope
		}
		a.bufferCodexModelText(delta.ItemID, delta.ThreadID, delta.Delta, AssembledMessageKindPlan, joinVerbatim, true)
	}
}

func (a *CodexAgent) handleReasoningSummaryTextDelta(params json.RawMessage) {
	if notif, ok := parseCodexModelDelta(params); ok && notif.ItemID != "" {
		a.observeReasoningText(notif.ItemID, codexReasoningKindSummary, notif.ThreadID, notif.Delta, notif.SummaryIndex)
	}
}

func (a *CodexAgent) handleReasoningSummaryPartAdded(params json.RawMessage) {
	var notif struct {
		ItemID       string `json:"itemId"`
		ThreadID     string `json:"threadId"`
		SummaryIndex *int   `json:"summaryIndex"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" {
		a.markReasoningSummaryBreak(notif.ItemID, notif.ThreadID, notif.SummaryIndex)
	}
}

func (a *CodexAgent) handleReasoningTextDelta(params json.RawMessage) {
	if notif, ok := parseCodexModelDelta(params); ok && notif.ItemID != "" {
		a.observeReasoningText(notif.ItemID, codexReasoningKindRaw, notif.ThreadID, notif.Delta, nil)
	}
}

func (a *CodexAgent) handleCommandExecutionOutputDelta(params json.RawMessage) {
	a.handleCodexToolOutputDelta(params)
}

func (a *CodexAgent) handleCommandExecutionTerminalInteraction(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Stdin  string `json:"stdin"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Stdin != "" {
		a.mu.Lock()
		a.appendCodexToolEventLocked(notif.ItemID, notif.Stdin, true)
		a.mu.Unlock()
	}
}

func (a *CodexAgent) handleFileChangeOutputDelta(params json.RawMessage) {
	a.handleCodexToolOutputDelta(params)
}

func (a *CodexAgent) handleCodexToolOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.appendCodexToolOutput(notif.ItemID, notif.Delta)
		if childSink, childOwned := a.childSinkForItem(notif.ItemID); childOwned {
			if childSink != nil {
				childSink.ReportProgress(OutputDeltaProgress(notif.ItemID, int64(len([]byte(notif.Delta)))))
			}
			return
		}
		a.sink.ReportProgress(OutputDeltaProgress(notif.ItemID, int64(len([]byte(notif.Delta)))))
	}
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
func (a *CodexAgent) handleItemStarted(raw []byte, params json.RawMessage) {
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
	a.handleCodexItemStartedForSink(a.sink, a.agentID, true, event)
}

func (a *CodexAgent) handleCodexItemStartedForSink(
	sink ProviderServices,
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
	case "agentMessage":
		// Wait for the authoritative completed item.
	case "contextCompaction":
		if mainThread {
			a.mu.Lock()
			if a.compactionStartAck != nil {
				close(a.compactionStartAck)
				a.compactionStartAck = nil
			}
			a.mu.Unlock()
		}
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
			slog.Error("codex persist compacting notification", "agent_id", agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView", "reasoning":
		persistSharedItemStarted(sink, event.params, event.itemType, event.itemID, agentID)
	case "collabAgentToolCall":
		collab := parseCollabToolCall(event.item)
		spawns := collab != nil && collab.Tool == codexCollabToolSpawnAgent
		if err := openToolSpan(sink, MessageContent{Original: event.params}, event.itemID, event.itemType, spawns); err != nil {
			slog.Error("codex persist collabAgentToolCall/started", "agent_id", agentID, "error", err)
		}
		a.registerCollabReceivers(collab, event.itemID, event.threadID)
	}
}

// handleItemCompleted processes item/completed notifications.
func (a *CodexAgent) handleItemCompleted(raw []byte, params json.RawMessage) {
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
		return
	}
	a.handleCodexItemCompletedForSink(a.sink, a.agentID, true, event)
}

func (a *CodexAgent) handleCodexItemCompletedForSink(
	sink ProviderServices,
	agentID string,
	mainThread bool,
	event codexItemEvent,
) {
	a.mu.Lock()
	delete(a.incompleteTools, event.itemID)
	delete(a.collabChildItems, event.itemID)
	a.mu.Unlock()
	if mainThread {
		discardCompletedCodexGeneration(&a.generationBuffer, event.itemType, event.itemID)
	} else {
		discardCompletedCodexGeneration(a.codexChildGenerationBuffer(event.threadID), event.itemType, event.itemID)
	}

	switch event.itemType {
	case "agentMessage":
		persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
	case "plan":
		if !mainThread {
			persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
			return
		}
		a.mu.Lock()
		a.turnSawPlan = true
		a.mu.Unlock()
		sink.ReportProgress(CompleteModelProgress(event.itemID))

		var planItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.item, &planItem) == nil && planItem.Text != "" {
			a.mu.Lock()
			a.turnPlanText = planItem.Text
			a.mu.Unlock()
		}
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: event.params}, SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType,
		}); err != nil {
			slog.Error("codex persist plan", "agent_id", agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView":
		if mainThread {
			a.mu.Lock()
			a.turnToolUses++
			a.mu.Unlock()
		}
		persistSharedItemCompleted(sink, event.params, event.itemType, event.itemID, agentID)
	case "collabAgentToolCall":
		collab := parseCollabToolCall(event.item)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: event.params}, SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/completed", "agent_id", agentID, "error", err)
		}
		sink.CloseSpan(event.itemID)
		if collab != nil {
			a.registerCollabReceivers(collab, event.itemID, event.threadID)
			a.collabAgentsStatesToRegistry(collab)
		}
	case "reasoning":
		a.persistCompletedReasoningItem(sink, event.params, event.itemID, agentID)
	case "contextCompaction":
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
			slog.Error("codex persist contextCompaction/completed", "agent_id", agentID, "error", err)
		}
	default:
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: event.params}, SpanInfo{
			SpanID: event.itemID, SpanType: event.itemType,
		}); err != nil {
			slog.Error("codex persist unknown item", "agent_id", agentID, "type", event.itemType, "error", err)
		}
	}
}

// handleTurnCompleted processes turn/completed notifications.
func (a *CodexAgent) handleTurnCompleted(params json.RawMessage) {
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
	a.mu.Lock()
	numToolUses := a.turnToolUses + incompleteToolUses
	sawPlan := a.turnSawPlan
	planText := a.turnPlanText
	collaborationMode := a.collaborationMode
	a.mu.Unlock()

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
	a.mu.Lock()
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.mu.Unlock()
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
	if err := a.sink.PersistTurnEnd(withToolUseCount(MessageContent{Original: params}, numToolUses), SpanInfo{}); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.agentID, "error", err)
	}

	// Reset all span tracking at turn-end so the next turn starts clean.
	// The child routes stay here because background tasks outlive root turns.
	// A completed child run keeps its route for later input. ClearContext or a
	// process restart removes the routes for the old child tree.
	a.sink.ResetSpans()

	if turnStatus != "" {
		retryable := turnStatus == "failed" &&
			(turnErrorInfo == "serverOverloaded" || isRetryableCodexTurnFailure(turnErrorMessage))
		scheduleOrCancelAPIErrorAutoContinue(a.sink, retryable, params)
		if turnStatus == "completed" && collaborationMode == CodexCollaborationPlan && sawPlan && planText != "" {
			// Persist plan content so initiatePlanExecution can use it.
			compressed, compression := msgcodec.Compress([]byte(planText))
			a.sink.UpdatePlan(compressed, compression, extractPlanTitle(planText))
			requestID := fmt.Sprintf("codex-plan-prompt-%s", turnID)
			payload, err := json.Marshal(map[string]interface{}{
				"type":       "control_request",
				"request_id": requestID,
				"request": map[string]interface{}{
					"tool_name": ToolNameCodexPlanModePrompt,
					"input":     map[string]interface{}{},
				},
			})
			if err == nil {
				if err := a.sink.PublishControlRequest(ControlRequest{RequestID: requestID, Payload: payload}); err != nil {
					slog.Error("publish plan approval", "agent_id", a.agentID, "request_id", requestID, "error", err)
				}
			}
		}
	}
}

func (a *CodexAgent) handleChildTurnCompleted(threadID string, params json.RawMessage, route codexChildRoute) {
	completion := codexTurnCompletion(params)
	a.flushCodexChildGeneration(threadID, completion)
	a.persistIncompleteCodexTools(threadID, false, completion)
	if err := route.childSink.PersistTurnEnd(MessageContent{Original: params}, SpanInfo{}); err != nil {
		slog.Warn("codex persist child turn/completed", "agent_id", a.agentID, "thread", threadID, "error", err)
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
		logRegistryRefusal("codex", "update status",
			a.sink.UpdateBackgroundTaskStatus(threadID, bgtask.StatusRunning, transition.activity))
	}
}

func (a *CodexAgent) flushCodexGeneration(completion MessageCompletion) {
	a.persistCodexGeneration(&a.generationBuffer, a.sink, completion)
}

func (a *CodexAgent) flushCodexChildGeneration(threadID string, completion MessageCompletion) {
	route, ok := a.lookupCodexChildRoute(threadID)
	if !ok {
		return
	}
	a.persistCodexGeneration(a.codexChildGenerationBuffer(threadID), route.childSink, completion)
}

func (a *CodexAgent) flushAllCodexGeneration(completion MessageCompletion) {
	a.flushCodexGeneration(completion)
	a.mu.Lock()
	threadIDs := make([]string, 0, len(a.collabChildren))
	for threadID, state := range a.collabChildren {
		if state != nil && state.childAgentID != "" {
			threadIDs = append(threadIDs, threadID)
		}
	}
	a.mu.Unlock()
	sort.Strings(threadIDs)
	for _, threadID := range threadIDs {
		a.flushCodexChildGeneration(threadID, completion)
	}
}

func (a *CodexAgent) persistCodexGeneration(buffer *GenerationBuffer, sink generationServices, completion MessageCompletion) {
	if a.isDiscardingOutput() {
		buffer.Reset()
		sink.ReportProgress(ResetModelProgress())
		return
	}
	if err := buffer.PersistAll(completion, func(raw []byte) error {
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: raw}, SpanInfo{})
	}); err != nil {
		slog.Error("codex persist partial generation", "agent_id", a.agentID, "error", err)
	}
}

func codexTurnCompletion(params json.RawMessage) MessageCompletion {
	var value struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &value) != nil {
		return MessageCompletionError
	}
	switch strings.ToLower(value.Turn.Status) {
	case "completed":
		return MessageCompletionComplete
	case "cancelled", "canceled", "interrupted", "aborted":
		return MessageCompletionInterrupted
	default:
		return MessageCompletionError
	}
}

func (a *CodexAgent) rememberCodexIncompleteTool(itemID, itemType, childThreadID string, params json.RawMessage) {
	if itemID == "" {
		return
	}
	a.mu.Lock()
	if a.incompleteTools == nil {
		a.incompleteTools = make(map[string]*codexIncompleteTool)
	}
	if a.incompleteTools[itemID] != nil {
		a.mu.Unlock()
		return
	}
	a.incompleteTools[itemID] = &codexIncompleteTool{
		params:        append(json.RawMessage(nil), params...),
		itemType:      itemType,
		childThreadID: childThreadID,
		order:         a.incompleteToolOrder,
	}
	a.incompleteToolOrder++
	a.mu.Unlock()
}

func (a *CodexAgent) appendCodexToolOutput(itemID, output string) {
	a.mu.Lock()
	a.appendCodexToolEventLocked(itemID, output, false)
	a.mu.Unlock()
}

func (a *CodexAgent) appendCodexToolEventLocked(itemID, text string, stdin bool) {
	tool := a.incompleteTools[itemID]
	if tool == nil || text == "" {
		return
	}
	if stdin {
		text = "> " + text
	}
	remaining := codexIncompleteOutputLimit - tool.outputBytes
	if remaining <= 0 {
		tool.outputTruncated = true
		return
	}
	if len(text) > remaining {
		text = text[:remaining]
		tool.outputTruncated = true
	}
	tool.outputEvents = append(tool.outputEvents, codexToolOutputEvent{text: text})
	tool.outputBytes += len(text)
}

func (a *CodexAgent) persistIncompleteCodexTools(childThreadID string, all bool, completion MessageCompletion) int {
	a.mu.Lock()
	itemIDs := make([]string, 0, len(a.incompleteTools))
	tools := make(map[string]codexIncompleteToolSnapshot)
	for itemID, tool := range a.incompleteTools {
		if tool == nil || (!all && tool.childThreadID != childThreadID) {
			continue
		}
		itemIDs = append(itemIDs, itemID)
		tools[itemID] = codexIncompleteToolSnapshot{
			params:          append(json.RawMessage(nil), tool.params...),
			itemType:        tool.itemType,
			childThreadID:   tool.childThreadID,
			output:          renderCodexToolOutput(tool.outputEvents, tool.outputTruncated),
			outputTruncated: tool.outputTruncated,
			order:           tool.order,
		}
		delete(a.incompleteTools, itemID)
		delete(a.collabChildItems, itemID)
	}
	a.mu.Unlock()
	if a.isDiscardingOutput() {
		return 0
	}
	retainedCompletion := completion
	if retainedCompletion == MessageCompletionComplete {
		retainedCompletion = MessageCompletionInterrupted
	}
	sort.Slice(itemIDs, func(left, right int) bool {
		leftTool, rightTool := tools[itemIDs[left]], tools[itemIDs[right]]
		if leftTool.order != rightTool.order {
			return leftTool.order < rightTool.order
		}
		return itemIDs[left] < itemIDs[right]
	})
	for _, itemID := range itemIDs {
		tool := tools[itemID]
		raw, err := buildIncompleteCodexTool(tool)
		if err != nil {
			slog.Warn("marshal incomplete codex tool", "agent_id", a.agentID, "item_id", itemID, "error", err)
			continue
		}
		sink := a.sink
		if tool.childThreadID != "" {
			route, ok := a.ensureCodexChildRoute(tool.childThreadID)
			if !ok {
				slog.Warn("persist incomplete codex child tool: route missing", "agent_id", a.agentID, "thread", tool.childThreadID)
				continue
			}
			sink = route.childSink
		}
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: raw, Completion: retainedCompletion}, SpanInfo{
			SpanID: itemID, SpanType: tool.itemType, Closing: true,
		}); err != nil {
			slog.Error("persist incomplete codex tool", "agent_id", a.agentID, "item_id", itemID, "error", err)
		}
		sink.CloseSpan(itemID)
		sink.ReportProgress(CompleteOutputProgress(itemID))
	}
	return len(itemIDs)
}

func buildIncompleteCodexTool(tool codexIncompleteToolSnapshot) ([]byte, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(tool.params, &params); err != nil {
		return nil, err
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(params["item"], &item); err != nil {
		return nil, err
	}
	if item == nil {
		return nil, fmt.Errorf("incomplete Codex tool has no item")
	}
	if output := tool.output; output != "" {
		item["aggregatedOutput"], _ = json.Marshal(output)
	}
	if tool.outputTruncated {
		item["outputTruncated"] = json.RawMessage(`true`)
	}
	encodedItem, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	params["item"] = encodedItem
	return json.Marshal(params)
}

func renderCodexToolOutput(events []codexToolOutputEvent, truncated bool) string {
	var output strings.Builder
	if truncated {
		output.WriteString(limitedOutputPrefix)
	}
	for _, event := range events {
		output.WriteString(event.text)
	}
	return output.String()
}

func codexItemIsTool(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView":
		return true
	default:
		return false
	}
}

func isRetryableCodexTurnFailure(message string) bool {
	return codexRetryableDisconnectPattern.MatchString(message)
}

// handleTokenUsageUpdated processes thread/tokenUsage/updated notifications.
func (a *CodexAgent) handleTokenUsageUpdated(content []byte, params json.RawMessage) {
	var notif struct {
		ThreadID   string `json:"threadId"`
		TurnID     string `json:"turnId"`
		TokenUsage struct {
			Last struct {
				InputTokens       int64 `json:"inputTokens"`
				CachedInputTokens int64 `json:"cachedInputTokens"`
				OutputTokens      int64 `json:"outputTokens"`
			} `json:"last"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex token_usage_updated unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}

	// Persist the raw Codex notification so reconnect/catch-up can rehydrate
	// context usage from history. Codex-emitted metadata → AGENT source.
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("codex persist tokenUsage", "agent_id", a.agentID, "error", err)
	}

	// Codex reports the cached part inside InputTokens, so the uncached remainder
	// is the input the other providers report.
	usage := contextUsageMap(contextTokenCounts{
		Input:     max(notif.TokenUsage.Last.InputTokens-notif.TokenUsage.Last.CachedInputTokens, 0),
		CacheRead: notif.TokenUsage.Last.CachedInputTokens,
		Output:    notif.TokenUsage.Last.OutputTokens,
	})
	if notif.TokenUsage.ModelContextWindow != nil {
		usage[contracts.ContextUsageFieldContextWindow] = *notif.TokenUsage.ModelContextWindow
	} else if cw := modelContextWindow(a.availableModels, a.model); cw > 0 {
		usage[contracts.ContextUsageFieldContextWindow] = cw
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyContextUsage: usage,
	})
}

func (a *CodexAgent) isMainThreadID(threadID string) bool {
	if threadID == "" {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return threadID == a.threadID
}

func (a *CodexAgent) isRetiredCodexOutput(params json.RawMessage) bool {
	var value struct {
		ThreadID string `json:"threadId"`
	}
	return json.Unmarshal(params, &value) == nil && a.isRetiredCodexThread(value.ThreadID)
}

func (a *CodexAgent) isRetiredCodexThread(threadID string) bool {
	if threadID == "" {
		return false
	}
	a.mu.Lock()
	_, retired := a.retiredCodexThreads[threadID]
	a.mu.Unlock()
	return retired
}

// handleApprovalRequest processes server requests (approval requests from Codex).
// These arrive as JSON-RPC requests (with an "id" field) from the server.
// The id is already extracted from the outer envelope to avoid re-parsing content.
func (a *CodexAgent) handleApprovalRequest(id string, content []byte) {
	if id == "" {
		slog.Warn("codex approval request missing id", "agent_id", a.agentID)
		return
	}
	a.publishControlRequest(a.sink, id, content)
}

// handleServerRequestResolved processes serverRequest/resolved notifications.
// For user-initiated responses the control request is already deleted by the
// SendControlResponse handler, but this also covers agent-initiated
// resolutions (e.g. the agent moves on without waiting for user input).
func (a *CodexAgent) handleServerRequestResolved(params json.RawMessage) {
	var notif struct {
		RequestID json.Number `json:"requestId"`
	}
	if json.Unmarshal(params, &notif) == nil {
		requestID := notif.RequestID.String()
		a.sink.DeleteControlRequest(requestID)
		a.sink.BroadcastControlCancel(requestID)
	}
}

// handleErrorNotification processes error notifications.
func (a *CodexAgent) handleErrorNotification(params json.RawMessage) {
	var notif struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.Message != "" {
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			"type":  contracts.NotificationTypeAgentError,
			"error": notif.Message,
		})
	}
}

// handleRateLimitsUpdated processes account/rateLimits/updated notifications.
// The raw content is persisted as-is via PersistNotification, and converted
// rate limit info is broadcast via BroadcastSessionInfo for the live popover.
func (a *CodexAgent) handleRateLimitsUpdated(content []byte, params json.RawMessage) {
	// Persist the raw Codex notification — agent-emitted metadata, AGENT source.
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("codex persist rateLimits", "agent_id", a.agentID, "error", err)
	}

	// Extract and convert tiers for live session info broadcast.
	var notif struct {
		RateLimits struct {
			Primary   *codexRateLimitTier `json:"primary"`
			Secondary *codexRateLimitTier `json:"secondary"`
			// RateLimitReachedType is emitted by newer Codex app-server builds
			// (an Option in the v2 RateLimitSnapshot) and is absent on older
			// ones. It is the authoritative "an actual limit was hit" signal --
			// snake_case enum values like "rate_limit_reached" or
			// "workspace_owner_credits_depleted". When absent, auto-continue
			// falls back to the usedPercent>=100 heuristic.
			RateLimitReachedType *string `json:"rateLimitReachedType"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex rate limit unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	reachedType := ""
	if notif.RateLimits.RateLimitReachedType != nil {
		reachedType = *notif.RateLimits.RateLimitReachedType
	}

	summary := summarizeCodexRateLimits([]*codexRateLimitTier{notif.RateLimits.Primary, notif.RateLimits.Secondary}, reachedType)

	if len(summary.rateLimits) > 0 {
		a.sink.BroadcastSessionInfo(map[string]interface{}{
			contracts.SessionInfoKeyRateLimits: summary.rateLimits,
		})
	}

	if resumeReset := codexRateLimitResumeReset(reachedType, summary); resumeReset != nil {
		a.sink.ScheduleAutoContinue(AutoContinueSchedule{
			Reason:        AutoContinueReasonRateLimit,
			DueAt:         *resumeReset,
			SourcePayload: append([]byte(nil), content...),
		})
	} else {
		a.sink.CancelAutoContinue(AutoContinueReasonRateLimit)
	}
}

// codexRateLimitSummary is the derived view of a Codex rateLimits snapshot: the
// wire-shaped per-window map broadcast to the popover, plus the decision inputs
// the auto-continue resume reads. Kept as one value so the elevate (applied in
// place on rateLimits) and the resume can be unit-tested without an agent.
type codexRateLimitSummary struct {
	// rateLimits is the wire map (rate_limit_type -> info) for the session-info popover.
	rateLimits map[string]interface{}
	// latestExceededReset is the latest reset among windows already at >=100% that
	// carry a reset -- the authoritative resume time for an already-exhausted window.
	latestExceededReset *time.Time
	// latestReset is the latest reset among ALL windows -- the resume fallback when a
	// time-windowed block's binding window carries no reset of its own.
	latestReset *time.Time
	// bindingReset is the most-utilized window's own reset (nil when it has none).
	bindingReset *time.Time
}

// summarizeCodexRateLimits converts the primary/secondary tiers into the wire map
// and the elevate/resume decision inputs in a single pass, applying the popover
// elevate in place. Pure (no agent side effects) so the elevate and resume edges
// are unit-testable in isolation.
func summarizeCodexRateLimits(tiers []*codexRateLimitTier, reachedType string) codexRateLimitSummary {
	s := codexRateLimitSummary{rateLimits: map[string]interface{}{}}
	// anyExceeded tracks whether ANY window is already at >=100% (status "exceeded"),
	// reset or not -- the elevate gate below, matching the frontend's status-based gate.
	anyExceeded := false
	// Track the most-utilized window so a reached-type block whose usedPercent has
	// been integer-rounded just under 100 still resolves to a window (and its reset)
	// to wait on and to surface as "exceeded" in the popover.
	var bindingTierKey string
	bindingPct := -1.0
	for _, tier := range tiers {
		if tier == nil {
			continue
		}
		rlType := codexWindowToType(tier.WindowDurationMins)
		status := codexTierStatus(tier.UsedPercent)
		if status == codexRateLimitStatusExceeded {
			anyExceeded = true
		}
		info := map[string]interface{}{
			contracts.RateLimitFieldRateLimitType: rlType,
			contracts.RateLimitFieldUtilization:   float64(tier.UsedPercent) / 100,
			contracts.RateLimitFieldStatus:        status,
		}
		var tierReset *time.Time
		if tier.ResetsAt != nil {
			resetAt := time.Unix(*tier.ResetsAt, 0).UTC()
			tierReset = &resetAt
			info[contracts.RateLimitFieldResetsAt] = *tier.ResetsAt
			if s.latestReset == nil || resetAt.After(*s.latestReset) {
				s.latestReset = &resetAt
			}
			if status == codexRateLimitStatusExceeded {
				if s.latestExceededReset == nil || resetAt.After(*s.latestExceededReset) {
					s.latestExceededReset = &resetAt
				}
			}
		}
		if tier.UsedPercent > bindingPct {
			bindingPct = tier.UsedPercent
			bindingTierKey = rlType
			s.bindingReset = tierReset
		}
		s.rateLimits[rlType] = info
	}

	// When Codex's authoritative reached-type says a time-windowed rate limit is hit
	// but rounding kept every window just under 100, elevate the most-utilized window
	// to "exceeded" so the popover matches the auto-continue decision. Gate on "no
	// window already exceeded" (status >=100), NOT on "no exceeded window carries a
	// reset": a window at >=100% without a reset already reads as exceeded, so
	// elevating a DIFFERENT binding window there would disagree with the frontend
	// replay path, which depends only on the per-window status
	// (codexRateLimitsFromMessage, via codexTierToRateLimitInfo in
	// lib/rateLimitUtils.ts).
	if reachedType == codexRateLimitReachedTimeWindow && !anyExceeded && bindingTierKey != "" {
		if info, ok := s.rateLimits[bindingTierKey].(map[string]interface{}); ok {
			info[contracts.RateLimitFieldStatus] = codexRateLimitStatusExceeded
		}
	}
	return s
}

// codexRateLimitTier represents a single tier from Codex rate limit data.
type codexRateLimitTier struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// Codex synthesized rate-limit status values. Codex emits a raw usedPercent per
// window with no status string of its own, so we classify here. Kept in sync
// with the frontend's codexTierToRateLimitInfo so popover and notifications
// agree on the thresholds.
const (
	codexRateLimitStatusAllowed        = "allowed"
	codexRateLimitStatusAllowedWarning = "allowed_warning"
	codexRateLimitStatusExceeded       = "exceeded"
)

// codexRateLimitReachedTimeWindow is the one Codex rateLimitReachedType that
// lifts on the rolling-window timer and is therefore safe to auto-resume. The
// others ("workspace_*_credits_depleted", "workspace_*_usage_limit_reached") are
// billing/usage caps a reset timer won't clear, so they must not auto-continue.
const codexRateLimitReachedTimeWindow = contracts.CodexRateLimitReachedTimeWindow

// codexTierStatus classifies a window's usedPercent into the synthesized status
// vocabulary shared with the frontend.
func codexTierStatus(usedPercent float64) string {
	switch {
	case usedPercent >= 100:
		return codexRateLimitStatusExceeded
	case usedPercent >= 80:
		return codexRateLimitStatusAllowedWarning
	default:
		return codexRateLimitStatusAllowed
	}
}

// codexRateLimitResumeReset decides when (if ever) a Codex agent should
// auto-resume after a rate-limit snapshot, returning the reset time to wait for
// or nil to cancel any pending resume.
//
//   - reachedType == "" (older Codex without the signal): fall back to the
//     usedPercent>=100 heuristic and resume at the latest exhausted window's reset.
//   - reachedType == "rate_limit_reached": a time-windowed block. Resume at the
//     latest exhausted window's reset, or the most-utilized window's reset when
//     rounding kept every window just under 100. When the most-utilized window
//     carries no reset of its own, fall back to the latest reset ANY window
//     reported so a resumable block still resumes instead of being cancelled.
//   - any other reachedType (credits depleted / usage cap): do not resume -- the
//     block won't lift on the rolling-window timer, so waiting would just re-hit it.
func codexRateLimitResumeReset(reachedType string, s codexRateLimitSummary) *time.Time {
	switch reachedType {
	case "":
		return s.latestExceededReset
	case codexRateLimitReachedTimeWindow:
		if s.latestExceededReset != nil {
			return s.latestExceededReset
		}
		if s.bindingReset != nil {
			return s.bindingReset
		}
		return s.latestReset
	default:
		return nil
	}
}

// codexWindowToType maps a Codex window duration (minutes) to a rate limit type string.
func codexWindowToType(mins int) string {
	switch mins {
	case 300:
		return "five_hour"
	case 10080:
		return "seven_day"
	default:
		if mins >= 1440 {
			days := (mins + 720) / 1440 // round
			return fmt.Sprintf("%d_day", days)
		}
		hours := (mins + 30) / 60 // round
		return fmt.Sprintf("%d_hour", hours)
	}
}

// codexCollabToolSpawnAgent is the one collabAgentToolCall tool that starts a
// subagent. Codex declares the whole set as spawnAgent, sendInput, resumeAgent,
// wait, closeAgent (CollabAgentTool in the app-server protocol); only the spawn
// owns no span.
const codexCollabToolSpawnAgent = "spawnAgent"

type codexCollabAgentState struct {
	Status string `json:"status"`
}

type codexCollabAgentToolCall struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
	// Prompt is the instruction the spawned agent was given. Codex declares it
	// `prompt: string | null` on the collabAgentToolCall thread item, so it is
	// absent for the non-spawn collab tools (send/wait).
	Prompt            string                           `json:"prompt"`
	ReceiverThreadIds []string                         `json:"receiverThreadIds"`
	AgentsStates      map[string]codexCollabAgentState `json:"agentsStates"`
}

func parseCollabToolCall(item json.RawMessage) *codexCollabAgentToolCall {
	var collab codexCollabAgentToolCall
	if err := json.Unmarshal(item, &collab); err != nil {
		slog.Warn("codex collab tool call unmarshal failed", "error", err)
		return nil
	}
	return &collab
}

// registerCollabReceiver records one legacy spawn route. Multi-Agent V2 later
// replaces these identity fields from its authoritative started activity.
func (a *CodexAgent) registerCollabReceiver(threadID, spawnCorrelationID, parentThreadID string) bool {
	if threadID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.codexChildStateLocked(threadID)
	changed := state.phase != codexChildRunning
	if state.spawnCorrelationID == "" && spawnCorrelationID != "" {
		state.spawnCorrelationID = spawnCorrelationID
		changed = true
	}
	if state.parentThreadID == "" {
		if parentThreadID == "" {
			parentThreadID = a.threadID
		}
		state.parentThreadID = parentThreadID
		changed = true
	}
	if state.phase != codexChildClosing {
		a.activateCodexChildStateLocked(state)
	}
	return changed
}

func (a *CodexAgent) registerCollabReceivers(
	collab *codexCollabAgentToolCall,
	spawnCorrelationID, parentThreadID string,
) {
	if collab == nil {
		return
	}
	for _, receiverID := range collab.ReceiverThreadIds {
		if collab.Tool == codexCollabToolSpawnAgent {
			a.recordCollabChildPromptTitle(receiverID, collab.Prompt)
			a.rememberCollabChildPrompt(receiverID, collab.Prompt)
			a.registerCollabReceiver(receiverID, spawnCorrelationID, parentThreadID)
			continue
		}
		// A wait, message, resume, or close call proves activity only. Its call
		// ID and sender do not identify the receiver's spawn or direct parent.
		a.activateCollabChild(receiverID)
	}
}

// lookupCodexChildRoute reads an existing route without creating transcript
// state. Call ensureCodexChildRoute when the current lifecycle event permits
// child creation.
func (a *CodexAgent) lookupCodexChildRoute(threadID string) (codexChildRoute, bool) {
	if threadID == "" || a.isMainThreadID(threadID) {
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	state, ok := a.collabChildren[threadID]
	if !ok || state == nil || state.childAgentID == "" {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	childAgentID := state.childAgentID
	parentThreadID := state.parentThreadID
	a.mu.Unlock()
	parentSink, parentAgentID, ok := a.codexServicesForThread(parentThreadID)
	if !ok {
		return codexChildRoute{}, false
	}
	return codexChildRoute{
		agentID:       childAgentID,
		parentAgentID: parentAgentID,
		parentSink:    parentSink,
		childSink:     parentSink.ChildSink(childAgentID),
	}, true
}

func (a *CodexAgent) ensureCodexChildRoute(threadID string) (codexChildRoute, bool) {
	if route, ok := a.lookupCodexChildRoute(threadID); ok {
		return route, true
	}
	if threadID == "" || a.isMainThreadID(threadID) {
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state == nil || state.spawnCorrelationID == "" {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	spawnCorrelationID := state.spawnCorrelationID
	parentThreadID := state.parentThreadID
	title := state.displayTitle()
	rootThreadID := a.threadID
	a.mu.Unlock()
	parentSink, parentAgentID, ok := a.codexServicesForThread(parentThreadID)
	if !ok {
		return codexChildRoute{}, false
	}
	childID, err := parentSink.EnsureChildAgent(spawnCorrelationID, threadID, title)
	if err != nil {
		slog.Warn("codex route child ensure failed", "thread", threadID, "error", err)
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	if a.threadID != rootThreadID || a.collabChildren[threadID] != state {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	if state.childAgentID == "" {
		state.childAgentID = childID
	} else {
		childID = state.childAgentID
	}
	a.mu.Unlock()
	return codexChildRoute{
		agentID:       childID,
		parentAgentID: parentAgentID,
		parentSink:    parentSink,
		childSink:     parentSink.ChildSink(childID),
	}, true
}

// codexServicesForThread resolves a thread's transcript sink. It walks the
// recorded parent chain before it touches sink caches, so malformed cycles or
// incomplete nested routes fail without creating a sink under the wrong parent.
func (a *CodexAgent) codexServicesForThread(threadID string) (ProviderServices, string, bool) {
	a.mu.Lock()
	mainThreadID := a.threadID
	if threadID == "" {
		threadID = mainThreadID
	}
	if threadID == mainThreadID {
		a.mu.Unlock()
		return a.sink, a.agentID, true
	}
	var agentIDs []string
	seen := make(map[string]struct{})
	for threadID != "" && threadID != mainThreadID {
		if _, duplicate := seen[threadID]; duplicate {
			a.mu.Unlock()
			return nil, "", false
		}
		seen[threadID] = struct{}{}
		state, ok := a.collabChildren[threadID]
		if !ok || state == nil || state.childAgentID == "" {
			a.mu.Unlock()
			return nil, "", false
		}
		agentIDs = append(agentIDs, state.childAgentID)
		threadID = state.parentThreadID
		if threadID == "" {
			threadID = mainThreadID
		}
	}
	a.mu.Unlock()

	sink := a.sink
	for i := len(agentIDs) - 1; i >= 0; i-- {
		sink = sink.ChildSink(agentIDs[i])
	}
	return sink, agentIDs[0], true
}

func (a *CodexAgent) rememberCodexChildItemThread(itemID, threadID string) {
	if itemID == "" || threadID == "" {
		return
	}
	a.mu.Lock()
	if a.collabChildItems == nil {
		a.collabChildItems = make(map[string]string)
	}
	a.collabChildItems[itemID] = threadID
	a.mu.Unlock()
}

func (a *CodexAgent) enqueuePendingCodexChildEvent(threadID string, event codexPendingChildEvent) {
	if threadID == "" {
		return
	}
	size := len(event.raw) + len(event.params)
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if len(state.pendingEvents) >= codexPendingChildEventLimit ||
		state.pendingEventBytes+size > codexPendingChildEventBytesLimit {
		firstDrop := !state.pendingOutputDropped
		state.pendingOutputDropped = true
		a.mu.Unlock()
		if firstDrop {
			slog.Warn("codex pending child event limit reached", "thread", threadID)
		}
		return
	}
	state.pendingEvents = append(state.pendingEvents, event)
	state.pendingEventBytes += size
	a.mu.Unlock()
}

func (a *CodexAgent) replayPendingCodexChildEvents(threadID string, route codexChildRoute) {
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state == nil || len(state.pendingEvents) == 0 && !state.pendingOutputDropped {
		a.mu.Unlock()
		return
	}
	events := state.pendingEvents
	dropped := state.pendingOutputDropped
	state.pendingEvents = nil
	state.pendingEventBytes = 0
	state.pendingOutputDropped = false
	a.mu.Unlock()

	if dropped {
		route.childSink.PersistLeapMuxNotification(map[string]interface{}{
			"type":  contracts.NotificationTypeAgentError,
			"error": "Some Codex subagent events exceeded the pending-route limit.",
		})
	}
	for _, event := range events {
		switch event.kind {
		case codexPendingItemStarted, codexPendingItemCompleted:
			itemEvent, ok := newCodexItemEvent(event.raw, event.params)
			if !ok {
				continue
			}
			if event.kind == codexPendingItemStarted {
				a.handleCodexItemStartedForSink(route.childSink, route.agentID, false, itemEvent)
			} else {
				a.handleCodexItemCompletedForSink(route.childSink, route.agentID, false, itemEvent)
			}
		case codexPendingTurnStarted:
			var value struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(event.params, &value) == nil && value.Turn.ID != "" {
				a.handleChildTurnStarted(threadID, value.Turn.ID, route)
			}
		case codexPendingTurnCompleted:
			a.handleChildTurnCompleted(threadID, event.params, route)
		}
	}
}

func discardCompletedCodexGeneration(buffer *GenerationBuffer, itemType, itemID string) {
	buffer.Discard(itemID)
	switch itemType {
	case "agentMessage":
		buffer.Discard(codexAssistantFallbackScope)
	case "plan":
		buffer.Discard(codexPlanFallbackScope)
	}
}

// persistSharedItemStarted applies item starts that share parent and child behavior.
func persistSharedItemStarted(sink ToolSpanServices, params json.RawMessage, itemType, itemID, agentID string) {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView":
		if err := openToolSpan(sink, MessageContent{Original: params}, itemID, itemType, false); err != nil {
			slog.Error("codex persist item/started", "agent_id", agentID, "type", itemType, "error", err)
		}
	case "reasoning":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: params}, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist reasoning/started", "agent_id", agentID, "error", err)
		}
	}
}

// persistSharedItemCompleted applies item completions that need no agent state.
func persistSharedItemCompleted(sink toolLifecycleServices, params json.RawMessage, itemType, itemID, agentID string) {
	sink.ReportProgress(CompleteModelProgress(itemID))
	sink.ReportProgress(CompleteOutputProgress(itemID))
	switch itemType {
	case "agentMessage", "plan":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: params}, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist agentMessage/plan", "agent_id", agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: params}, SpanInfo{
			SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist item/completed", "agent_id", agentID, "type", itemType, "error", err)
		}
		sink.CloseSpan(itemID)
	}
}

// persistCompletedReasoningItem stores the provider's authoritative item.
func (a *CodexAgent) persistCompletedReasoningItem(sink generationServices, params json.RawMessage, itemID, agentID string) {
	sink.ReportProgress(CompleteModelProgress(itemID))
	err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: params}, SpanInfo{
		SpanID: itemID, SpanType: "reasoning",
	})
	a.mu.Lock()
	suffix := "\x00" + itemID
	for key := range a.reasoningStreamKind {
		if strings.HasSuffix(key, suffix) {
			delete(a.reasoningStreamKind, key)
			delete(a.reasoningRetainedKind, key)
			delete(a.reasoningSummaryIndex, key)
			delete(a.reasoningSummarySeen, key)
			delete(a.reasoningSummaryBreak, key)
		}
	}
	a.mu.Unlock()
	if err != nil {
		slog.Error("codex persist reasoning/completed", "agent_id", agentID, "error", err)
		return
	}
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
