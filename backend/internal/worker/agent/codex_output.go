package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var codexRetryableDisconnectPattern = regexp.MustCompile(`^stream disconnected before completion(?:$|[^[:alnum:]].*)`)

// codexSystemMetadataMethods are Codex-emitted JSON-RPC notifications that
// carry agent/system metadata (lifecycle, MCP startup, skills invalidation,
// remote-control status). They share one handler — persist verbatim as
// agent-emitted notifications. Methods with extra side effects
// (rate-limit, token-usage broadcasts) keep dedicated cases below.
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
		a.handleItemCompleted(line.Params)

	case "turn/completed":
		a.handleTurnCompleted(line.Params)

	case "thread/tokenUsage/updated":
		a.handleTokenUsageUpdated(line.Raw, line.Params)

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"item/tool/requestUserInput":
		a.handleApprovalRequest(line.IDString(), line.Raw)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(line.Params)

	case "account/rateLimits/updated":
		a.handleRateLimitsUpdated(line.Raw, line.Params)

	case "error":
		a.handleErrorNotification(line.Params)

	default:
		// Persist unknown notifications so the frontend can decide how to render them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw, SpanInfo{}); err != nil {
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
	if json.Unmarshal(params, &notif) == nil && notif.Turn.ID != "" {
		// A collab subagent's turn/started carries a child threadId. It must not
		// replace the primary turn ID used for turn/interrupt and turn/steer, and
		// the frontend has no counter clear for child turns. This mirrors
		// handleTurnCompleted's main-thread gate and observeMainThreadText.
		if !a.isMainThreadID(notif.ThreadID) {
			// Record the child's active turn id (for steering) and upsert a
			// running registry row so the child tab reflects activity.
			a.setChildTurnID(notif.ThreadID, notif.Turn.ID)
			if a.knownCollabChild(notif.ThreadID) {
				_ = a.sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey:     notif.ThreadID,
					Kind:       bgtask.KindSubagent,
					Title:      a.collabChildTitle(notif.ThreadID),
					ActiveForm: "working",
					Status:     bgtask.StatusRunning,
				})
			}
			return
		}
		a.mu.Lock()
		// Codex ACCEPTED the turn. This is what `sendTurnStart` waits on: the
		// turn/start response does not arrive until the turn ends.
		if a.turnStartAck != nil {
			close(a.turnStartAck)
			a.turnStartAck = nil
		}
		a.turnID = notif.Turn.ID
		a.turnToolUses = 0
		a.turnSawPlan = false
		a.turnPlanText = ""
		a.streamingPlan = false
		// Drop any per-item reasoning-stream locks from a prior turn so itemIds
		// can't leak across turns (e.g. a reasoning item left open by an abort).
		clear(a.reasoningStreamKind)
		a.mu.Unlock()
		// A fresh turn begins: restart the thinking-token estimate from zero. The
		// reset is lock-free (the estimator self-locks), so it stays outside the
		// critical section above.
		a.thinkingTokens.reset()

		// Broadcast the turn ID so the frontend can use it for interrupts.
		a.sink.BroadcastSessionInfo(map[string]interface{}{
			"codex_turn_id": notif.Turn.ID,
		})
	}
}

// observeMainThreadText feeds a streamed model-text delta into the thinking-token
// estimate, but only for the main thread. A collab subagent's deltas arrive
// through these same handlers carrying a child threadId; counting them would
// inflate the primary agent's counter with text the user attributes to a
// subagent. The visible stream chunk is still broadcast regardless of thread --
// only the token estimate is gated.
func (a *CodexAgent) observeMainThreadText(threadID, text string) {
	if a.isMainThreadID(threadID) {
		a.thinkingTokens.observe(a.sink, text)
	}
}

// childSinkForThread resolves the child OutputSink for a collab subagent's
// thread, or returns nil when the thread is the main thread or an unknown child.
// Streaming deltas (agentMessage, commandExecution output, reasoning) use this to
// route a subagent's live text to its OWN transcript instead of the parent's --
// without it, the child's content streams into the parent and is then duplicated
// when the finished item persists into the child.
func (a *CodexAgent) childSinkForThread(threadID string) OutputSink {
	if childID := a.routeChildItemIfApplicable(threadID); childID != "" {
		return a.sink.ChildSink(childID)
	}
	return nil
}

// childSinkForItem resolves the child OutputSink that owns a streamed item
// (commandExecution/fileChange), or nil when the item belongs to the parent.
// Output-delta notifications carry only an itemID (no threadId), so this map --
// populated by persistItemStartedChild -- is how those chunks reach the child.
func (a *CodexAgent) childSinkForItem(itemID string) OutputSink {
	if itemID == "" {
		return nil
	}
	a.mu.Lock()
	childID := a.collabChildItems[itemID]
	a.mu.Unlock()
	if childID == "" {
		return nil
	}
	return a.sink.ChildSink(childID)
}

// Codex reasoning sub-stream kinds. A single reasoning item can surface as a
// condensed summary stream and/or the raw reasoning stream, both under one
// itemId; observeReasoningText counts only the first-seen kind per item.
const (
	codexReasoningKindSummary = "summary"
	codexReasoningKindRaw     = "raw"
)

// observeReasoningText feeds a reasoning delta into the thinking-token estimate,
// counting only the FIRST reasoning sub-stream ("summary" or "raw") seen for a
// given reasoning itemId. Codex can stream both summaryTextDelta and textDelta
// for the SAME item -- the same generation surfaced two ways -- so counting both
// would roughly double the estimate. Locking onto whichever kind arrives first
// avoids the double count while still moving the counter for models that stream
// only one kind. The main-thread gate still applies via observeMainThreadText, so
// a subagent's reasoning (child threadId) is excluded regardless of kind.
func (a *CodexAgent) observeReasoningText(itemID, kind, threadID, text string) {
	if a.shouldCountReasoningKind(itemID, kind) {
		a.observeMainThreadText(threadID, text)
	}
}

// shouldCountReasoningKind records the first reasoning sub-stream kind seen for
// itemID and reports whether kind is that first-seen kind. Separating the locked
// map bookkeeping from observeReasoningText's delegation lets the lock use a plain
// defer: observeMainThreadText re-acquires a.mu (via isMainThreadID), so the
// inline form had to unlock manually before delegating. See observeReasoningText
// for why only the first-seen kind is counted.
func (a *CodexAgent) shouldCountReasoningKind(itemID, kind string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reasoningStreamKind == nil {
		a.reasoningStreamKind = make(map[string]string)
	}
	locked, ok := a.reasoningStreamKind[itemID]
	if !ok {
		a.reasoningStreamKind[itemID] = kind
		return true
	}
	return locked == kind
}

// handleAgentMessageDelta processes item/agentMessage/delta — streaming text.
func (a *CodexAgent) handleAgentMessageDelta(params json.RawMessage) {
	var delta struct {
		Delta    string `json:"delta"`
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &delta) == nil && delta.Delta != "" {
		if sink := a.childSinkForThread(delta.ThreadID); sink != nil {
			sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/agentMessage/delta")
		} else {
			a.sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/agentMessage/delta")
		}
		a.observeMainThreadText(delta.ThreadID, delta.Delta)
	}
}

// handlePlanDelta processes item/plan/delta — streaming plan text.
func (a *CodexAgent) handlePlanDelta(params json.RawMessage) {
	var delta struct {
		Delta    string `json:"delta"`
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &delta) == nil && delta.Delta != "" {
		// Plan streaming is main-thread only; a child's plan deltas route to
		// the child without the session-info side effect.
		if sink := a.childSinkForThread(delta.ThreadID); sink != nil {
			sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/plan/delta")
			a.observeMainThreadText(delta.ThreadID, delta.Delta)
			return
		}
		a.mu.Lock()
		if !a.streamingPlan {
			a.streamingPlan = true
			a.mu.Unlock()
			a.sink.BroadcastSessionInfo(map[string]interface{}{
				"streaming_type": "plan",
			})
		} else {
			a.mu.Unlock()
		}
		a.sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/plan/delta")
		a.observeMainThreadText(delta.ThreadID, delta.Delta)
	}
}

func (a *CodexAgent) handleReasoningSummaryTextDelta(params json.RawMessage) {
	var notif struct {
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		if sink := a.childSinkForThread(notif.ThreadID); sink != nil {
			sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/summaryTextDelta")
		} else {
			a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/summaryTextDelta")
		}
		a.observeReasoningText(notif.ItemID, codexReasoningKindSummary, notif.ThreadID, notif.Delta)
	}
}

func (a *CodexAgent) handleReasoningSummaryPartAdded(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" {
		a.sink.BroadcastStreamChunk(nil, notif.ItemID, "item/reasoning/summaryPartAdded")
	}
}

func (a *CodexAgent) handleReasoningTextDelta(params json.RawMessage) {
	var notif struct {
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		if sink := a.childSinkForThread(notif.ThreadID); sink != nil {
			sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/textDelta")
		} else {
			a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/textDelta")
		}
		a.observeReasoningText(notif.ItemID, codexReasoningKindRaw, notif.ThreadID, notif.Delta)
	}
}

func (a *CodexAgent) handleCommandExecutionOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		if sink := a.childSinkForItem(notif.ItemID); sink != nil {
			sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/commandExecution/outputDelta")
		} else {
			a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/commandExecution/outputDelta")
		}
	}
}

func (a *CodexAgent) handleCommandExecutionTerminalInteraction(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Stdin  string `json:"stdin"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Stdin != "" {
		if sink := a.childSinkForItem(notif.ItemID); sink != nil {
			sink.BroadcastStreamChunk([]byte(notif.Stdin), notif.ItemID, "item/commandExecution/terminalInteraction")
		} else {
			a.sink.BroadcastStreamChunk([]byte(notif.Stdin), notif.ItemID, "item/commandExecution/terminalInteraction")
		}
	}
}

func (a *CodexAgent) handleFileChangeOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		if sink := a.childSinkForItem(notif.ItemID); sink != nil {
			sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/fileChange/outputDelta")
		} else {
			a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/fileChange/outputDelta")
		}
	}
}

// handleItemStarted processes item/started notifications.
func (a *CodexAgent) handleItemStarted(raw []byte, params json.RawMessage) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return
	}

	// subAgentActivity (v2) is registry-only: never persist. Consume it here
	// before any transcript handling.
	if itemType == "subAgentActivity" {
		a.handleCodexSubAgentActivity(item)
		return
	}

	// Route child-thread items to the child transcript. A non-empty threadID
	// that is NOT the main thread identifies a collab child; resolve its child
	// agent id (EnsureChildAgent, idempotent) and run the same per-type persist/
	// span logic against the child sink.
	if childID := a.routeChildItemIfApplicable(threadID); childID != "" {
		a.persistItemStartedChild(childID, params, itemType, itemID)
		return
	}

	switch itemType {
	case "agentMessage":
		// No-op for started — wait for completed to persist.
	case "contextCompaction":
		// Persist the raw `item/started` JSON-RPC notification verbatim as
		// AGENT. Preserves both `method:"item/started"` (so the
		// notification consolidator and frontend classifier route by
		// method) and Codex-specific fields under `params.item.*` that a
		// synthesized `{type:"compacting"}` would discard.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
			slog.Error("codex persist compacting notification", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		persistToolItemStarted(a.sink, params, itemType, itemID, a.agentID)
	case "collabAgentToolCall":
		// The spawn tool_call stays in the parent transcript as a flat tool
		// span (children no longer nest under it). Register receiver threads
		// as the child index so later child-thread items route to child
		// transcripts.
		spanColor := a.sink.ReserveSpanColor(itemID, "")
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType, SpanColor: spanColor,
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/started", "agent_id", a.agentID, "error", err)
		}
		a.sink.SetSpanType(itemID, itemType)
		a.sink.OpenSpan(itemID, "")
		if collab := parseCollabToolCall(item); collab != nil {
			for _, receiverID := range collab.ReceiverThreadIds {
				a.registerCollabReceiver(receiverID, itemID)
				a.collabChildPrompts.remember(receiverID, collab.Prompt)
			}
		}
	case "reasoning":
		// No-op for started — wait for completed.
	}
}

// handleItemCompleted processes item/completed notifications.
func (a *CodexAgent) handleItemCompleted(params json.RawMessage) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return
	}

	// subAgentActivity (v2) is registry-only: never persist. Consume it here
	// before any transcript handling.
	if itemType == "subAgentActivity" {
		a.handleCodexSubAgentActivity(item)
		return
	}

	// Route child-thread items to the child transcript (mirrors
	// handleItemStarted).
	if childID := a.routeChildItemIfApplicable(threadID); childID != "" {
		a.persistItemCompletedChild(childID, params, itemType, itemID)
		return
	}

	switch itemType {
	case "agentMessage":
		persistToolItemCompleted(a.sink, params, itemType, itemID, a.agentID)
	case "plan":
		a.mu.Lock()
		a.turnSawPlan = true
		wasStreamingPlan := a.streamingPlan
		a.streamingPlan = false
		a.mu.Unlock()
		if wasStreamingPlan {
			a.sink.BroadcastSessionInfo(map[string]interface{}{
				"streaming_type": "",
			})
		}

		var planItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &planItem) == nil && planItem.Text != "" {
			a.mu.Lock()
			a.turnPlanText = planItem.Text
			a.mu.Unlock()
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist plan", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		a.mu.Lock()
		a.turnToolUses++
		a.mu.Unlock()
		persistToolItemCompleted(a.sink, params, itemType, itemID, a.agentID)
	case "collabAgentToolCall":
		// Every collab tool_call (spawnAgent, wait, sendInput, resumeAgent,
		// closeAgent) is a flat tool span in the parent transcript that CLOSES at
		// completion. Children no longer nest under the spawn span -- they route
		// to their own transcripts -- so leaving any of these open leaks a span
		// until the next turn's bulk ResetSpans.
		var collab *codexCollabAgentToolCall
		if itemType == "collabAgentToolCall" {
			collab = parseCollabToolCall(item)
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/completed", "agent_id", a.agentID, "error", err)
		}
		a.sink.CloseSpan(itemID)
		// Register receiver threads on completion (late-binding, like the old
		// code) and drive the registry from agentsStates.
		if collab != nil {
			for _, receiverID := range collab.ReceiverThreadIds {
				a.registerCollabReceiver(receiverID, itemID)
				a.collabChildPrompts.remember(receiverID, collab.Prompt)
			}
			if collab.Tool == "spawnAgent" {
				if sp := collab.Prompt; sp != "" {
					for _, rid := range collab.ReceiverThreadIds {
						a.recordCollabChildTitle(rid, sp)
					}
				}
			}
			a.collabAgentsStatesToRegistry(collab)
		}
	case "reasoning":
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist reasoning", "agent_id", a.agentID, "error", err)
		}
		a.mu.Lock()
		delete(a.reasoningStreamKind, itemID)
		a.mu.Unlock()
		a.sink.BroadcastStreamEnd(itemID)
	case "contextCompaction":
		// No-op: completion is represented by thread/compacted.
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist unknown item", "agent_id", a.agentID, "type", itemType, "error", err)
		}
	}
}

// handleTurnCompleted processes turn/completed notifications.
func (a *CodexAgent) handleTurnCompleted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &notif) == nil && !a.isMainThreadID(notif.ThreadID) {
		// Child turn ended: persist a turn-end divider into the child
		// transcript (mirrors the main-thread PersistTurnEnd below), then clear
		// its active turn id (steering no longer possible until a new child
		// turn starts). Falls through silently when the thread is unknown to
		// the child index (e.g. a late receiver the spawn never registered).
		if childID := a.routeChildItemIfApplicable(notif.ThreadID); childID != "" {
			if err := a.sink.PersistChildTurnEnd(childID, params, SpanInfo{}); err != nil {
				slog.Warn("codex persist child turn/completed", "agent_id", a.agentID, "thread", notif.ThreadID, "error", err)
			}
		}
		a.clearChildTurnID(notif.ThreadID)
		return
	}

	// Enrich the params with num_tool_uses so the frontend can distinguish
	// simple text-only exchanges from complex multi-tool turns.
	a.mu.Lock()
	numToolUses := a.turnToolUses
	sawPlan := a.turnSawPlan
	planText := a.turnPlanText
	collaborationMode := a.collaborationMode
	a.mu.Unlock()

	// Parse once: enrich with num_tool_uses and extract turn data
	// from the same map to avoid a second json.Unmarshal.
	var turnStatus, turnID, turnErrorMessage, turnErrorInfo string
	parsed := make(map[string]json.RawMessage)
	if err := json.Unmarshal(params, &parsed); err == nil {
		if b, err := json.Marshal(numToolUses); err == nil {
			parsed["num_tool_uses"] = b
		}
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
		if b, err := json.Marshal(parsed); err == nil {
			params = json.RawMessage(b)
		}
	}

	// Persist as a result divider.
	if err := a.sink.PersistTurnEnd(params, SpanInfo{}); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.agentID, "error", err)
	}

	// Reset all span tracking at turn-end so the next turn starts clean.
	// The collab child index is NOT cleared here: background tasks outlive
	// turns, and the child-index entries are removed only on terminal collab
	// status (collabAgentsStatesToRegistry). Clearing on ClearContext/restart
	// stays.
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
				claimToken := a.sink.PersistControlRequest(requestID, payload)
				a.sink.BroadcastControlRequest(requestID, payload, claimToken)
			}
		}
	}

	a.mu.Lock()
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.mu.Unlock()

	// Clear the turn ID in session info.
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"codex_turn_id": "",
	})
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

	usage := map[string]interface{}{
		"input_tokens":                max(notif.TokenUsage.Last.InputTokens-notif.TokenUsage.Last.CachedInputTokens, 0),
		"cache_creation_input_tokens": int64(0),
		"cache_read_input_tokens":     notif.TokenUsage.Last.CachedInputTokens,
		"output_tokens":               notif.TokenUsage.Last.OutputTokens,
	}
	if notif.TokenUsage.ModelContextWindow != nil {
		usage["context_window"] = *notif.TokenUsage.ModelContextWindow
	} else if cw := modelContextWindow(a.availableModels, a.model); cw > 0 {
		usage["context_window"] = cw
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"context_usage": usage,
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

// handleApprovalRequest processes server requests (approval requests from Codex).
// These arrive as JSON-RPC requests (with an "id" field) from the server.
// The id is already extracted from the outer envelope to avoid re-parsing content.
func (a *CodexAgent) handleApprovalRequest(id string, content []byte) {
	if id == "" {
		slog.Warn("codex approval request missing id", "agent_id", a.agentID)
		return
	}
	claimToken := a.sink.PersistControlRequest(id, content)
	a.sink.BroadcastControlRequest(id, content, claimToken)
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
			"type":  NotificationTypeAgentError,
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
			"rate_limits": summary.rateLimits,
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
			"rate_limit_type": rlType,
			"utilization":     float64(tier.UsedPercent) / 100,
			"status":          status,
		}
		var tierReset *time.Time
		if tier.ResetsAt != nil {
			resetAt := time.Unix(*tier.ResetsAt, 0).UTC()
			tierReset = &resetAt
			info["resets_at"] = *tier.ResetsAt
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
	// replay path, which gates purely on the per-window status (extractRateLimitInfo).
	if reachedType == codexRateLimitReachedTimeWindow && !anyExceeded && bindingTierKey != "" {
		if info, ok := s.rateLimits[bindingTierKey].(map[string]interface{}); ok {
			info["status"] = codexRateLimitStatusExceeded
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
const codexRateLimitReachedTimeWindow = "rate_limit_reached"

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

// registerCollabReceiver records a child thread -> spawnSpanID mapping (the
// child index). Idempotent. The index is NOT cleared at turn end (background
// tasks outlive turns); entries are removed on terminal collab status by
// collabAgentsStatesToRegistry -> removeCollabChildIndex.
func (a *CodexAgent) registerCollabReceiver(threadID, spanID string) bool {
	if threadID == "" || spanID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans == nil {
		a.collabThreadSpans = make(map[string]string)
	}
	if a.collabThreadSpans[threadID] == spanID {
		return false
	}
	a.collabThreadSpans[threadID] = spanID
	return true
}

// routeChildItemIfApplicable returns the child agent id when the threadID is a
// non-main collab child known to the index, resolving it via EnsureChildAgent
// (idempotent). Returns "" for the main thread, an empty thread, or an unknown
// child (which falls through to the parent handler; the index may learn the
// mapping later via registerCollabReceiver).
func (a *CodexAgent) routeChildItemIfApplicable(threadID string) string {
	if threadID == "" {
		return ""
	}
	a.mu.Lock()
	mainThreadID := a.threadID
	spanID := a.collabThreadSpans[threadID]
	a.mu.Unlock()
	if threadID == mainThreadID {
		return ""
	}
	if spanID == "" {
		return ""
	}
	childID, err := a.sink.EnsureChildAgent(spanID, threadID, a.collabChildTitle(threadID))
	if err != nil {
		slog.Warn("codex route child ensure failed", "thread", threadID, "error", err)
		return ""
	}
	return childID
}

// persistItemStartedChild runs the item/started per-type persist/span logic
// against the child sink (started opens a span on the child transcript).
func (a *CodexAgent) persistItemStartedChild(childID string, params json.RawMessage, itemType, itemID string) {
	// Record the itemID -> childID mapping so output-delta handlers (which
	// carry only an itemID) can route streaming chunks to the child.
	if itemID != "" {
		a.mu.Lock()
		if a.collabChildItems == nil {
			a.collabChildItems = make(map[string]string)
		}
		a.collabChildItems[itemID] = childID
		a.mu.Unlock()
	}
	childSink := a.sink.ChildSink(childID)
	persistToolItemStarted(childSink, params, itemType, itemID, childID)
}

// persistItemCompletedChild runs the item/completed per-type persist/span logic
// against the child sink (completed closes the span on the child transcript).
func (a *CodexAgent) persistItemCompletedChild(childID string, params json.RawMessage, itemType, itemID string) {
	// The item is done streaming; drop it from the itemID -> child index so the
	// map doesn't grow unbounded across turns.
	if itemID != "" {
		a.mu.Lock()
		delete(a.collabChildItems, itemID)
		a.mu.Unlock()
	}
	childSink := a.sink.ChildSink(childID)
	persistToolItemCompleted(childSink, params, itemType, itemID, childID)
}

// persistToolItemStarted runs the shared per-type item/started logic for a tool
// span (commandExecution/fileChange/mcpToolCall/dynamicToolCall) against the
// given sink. Used by both the parent path (a.sink) and the child path
// (childSink). Non-tool item types are no-ops here (the parent handles its own
// agentMessage/contextCompaction/collabAgentToolCall/reasoning cases inline).
func persistToolItemStarted(sink OutputSink, params json.RawMessage, itemType, itemID, agentID string) {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		spanColor := sink.ReserveSpanColor(itemID, "")
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType, SpanColor: spanColor,
		}); err != nil {
			slog.Error("codex persist item/started", "agent_id", agentID, "type", itemType, "error", err)
		}
		sink.SetSpanType(itemID, itemType)
		sink.OpenSpan(itemID, "")
	}
}

// persistToolItemCompleted runs the shared per-type item/completed logic for
// tool and message spans against the given sink. Used by both the parent path
// and the child path. The parent keeps its own collabAgentToolCall/contextCompaction
// handling inline (they carry parent-only orchestration the child never sees).
func persistToolItemCompleted(sink OutputSink, params json.RawMessage, itemType, itemID, agentID string) {
	switch itemType {
	case "agentMessage", "plan":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist agentMessage/plan", "agent_id", agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist item/completed", "agent_id", agentID, "type", itemType, "error", err)
		}
		if itemType == "commandExecution" || itemType == "fileChange" {
			sink.BroadcastStreamEnd(itemID)
		}
		sink.CloseSpan(itemID)
	case "reasoning":
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, params, SpanInfo{
			SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist reasoning", "agent_id", agentID, "error", err)
		}
		sink.BroadcastStreamEnd(itemID)
	}
}

// extractCodexItem extracts the item type, ID, and threadId from item/started
// or item/completed params. The threadId is returned alongside the item to
// avoid a redundant unmarshal in codexParentSpanID.
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
