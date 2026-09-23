package codex

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// This file owns streamed model text, reasoning deltas, and tool-output deltas.

func (a *Agent) clearReasoningStateForThread(threadID string) {
	prefix := threadID + "\x00"
	a.Mu.Lock()
	for key := range a.reasoningStreamKind {
		if strings.HasPrefix(key, prefix) {
			delete(a.reasoningStreamKind, key)
			delete(a.reasoningRetainedKind, key)
			delete(a.reasoningSummaryIndex, key)
			delete(a.reasoningSummarySeen, key)
			delete(a.reasoningSummaryBreak, key)
		}
	}
	a.Mu.Unlock()
}

// bufferCodexModelText routes one model delta to its counter and buffer.
func (a *Agent) bufferCodexModelText(
	scopeID, threadID, text string,
	kind agent.AssembledMessageKind,
	join providerkit.TextJoin,
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
		sink.ReportProgress(agent.ModelTextProgress(scopeID, text))
	}
	buffer.Append(scopeID, kind, text, join)
}

func (a *Agent) reportCodexModelProgress(scopeID, threadID, text string) {
	sink := a.sink
	if !a.isMainThreadID(threadID) {
		route, routed := a.ensureCodexChildRoute(threadID)
		if !routed {
			return
		}
		a.replayPendingCodexChildEvents(threadID, route)
		sink = route.childSink
	}
	sink.ReportProgress(agent.ModelTextProgress(scopeID, text))
}

func (a *Agent) discardCodexModelText(scopeID, threadID string) {
	if a.isMainThreadID(threadID) {
		a.generationBuffer.Discard(scopeID)
		return
	}
	a.codexChildGenerationBuffer(threadID).Discard(scopeID)
}

func (a *Agent) codexChildGenerationBuffer(threadID string) *providerkit.GenerationBuffer {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return &a.codexChildStateLocked(threadID).generationBuffer
}

func (a *Agent) appendPendingCodexChildGeneration(
	threadID, scopeID string,
	kind agent.AssembledMessageKind,
	text string,
	join providerkit.TextJoin,
) {
	a.Mu.Lock()
	state := a.codexChildStateLocked(threadID)
	remaining := codexPendingChildGenerationLimit - state.pendingGenerationBytes
	if remaining < len(text) {
		firstDrop := !state.pendingOutputDropped
		state.pendingOutputDropped = true
		a.Mu.Unlock()
		if firstDrop {
			slog.Warn("codex pending child generation limit reached", "thread", threadID)
		}
		return
	}
	state.pendingGenerationBytes += len(text)
	buffer := &state.generationBuffer
	a.Mu.Unlock()
	buffer.Append(scopeID, kind, text, join)
}

func (a *Agent) clearPendingCodexChildGenerationBytes(threadID string) {
	a.Mu.Lock()
	if state := a.collabChildren[threadID]; state != nil {
		state.pendingGenerationBytes = 0
	}
	a.Mu.Unlock()
}

// childSinkForItem resolves the child services that own a tool item. Output
// deltas carry only an item ID, so collabChildItems restores the thread route.
func (a *Agent) childSinkForItem(itemID string) (agent.ProviderServices, bool) {
	if itemID == "" {
		return nil, false
	}
	a.Mu.Lock()
	threadID := a.collabChildItems[itemID]
	a.Mu.Unlock()
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
	// outputTail is the LAST bytes the call printed, kept beside the full event
	// list. The live card shows a window, and rebuilding the whole buffer to take
	// that window cost one copy of everything received so far on EVERY delta --
	// quadratic over a streaming build log, under the agent's own mutex, and the
	// publisher then clipped all but a couple of kilobytes of each copy.
	outputTail string
	order      uint64
}

type codexIncompleteToolSnapshot struct {
	params        json.RawMessage
	itemType      string
	childThreadID string
	// output is the joined delta text. A limited join already leads with the shared
	// truncation prefix, so the snapshot needs no flag of its own.
	output string
	order  uint64
}

// observeReasoningText feeds a reasoning delta into the token counter,
// counting only the FIRST reasoning sub-stream ("summary" or "raw") seen for a
// given reasoning itemId. Codex can stream both summaryTextDelta and textDelta
// for the SAME item -- the same generation surfaced two ways -- so counting both
// would roughly double the estimate. Locking onto whichever kind arrives first
// avoids the double count while still moving the counter for models that stream
// only one kind.
func (a *Agent) observeReasoningText(itemID, kind, threadID, text string, summaryIndex *int) {
	key := codexReasoningKey(threadID, itemID)
	a.Mu.Lock()
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
	join := providerkit.JoinVerbatim
	if kind == codexReasoningKindSummary && retainText {
		// summaryTextDelta is a verbatim delta inside one summary part.
		// summaryPartAdded and a changed summaryIndex start a new paragraph.
		if a.reasoningSummarySeen[key] && (a.reasoningSummaryBreak[key] ||
			summaryIndex != nil && *summaryIndex != a.reasoningSummaryIndex[key]) {
			join = providerkit.JoinParagraph
		}
		if summaryIndex != nil {
			a.reasoningSummaryIndex[key] = *summaryIndex
		}
		a.reasoningSummarySeen[key] = true
		a.reasoningSummaryBreak[key] = false
	}
	a.Mu.Unlock()

	if resetRetained {
		a.discardCodexModelText(itemID, threadID)
	}
	if retainText {
		a.bufferCodexModelText(itemID, threadID, text, agent.AssembledMessageKindReasoning, join, countKind == kind)
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

func (a *Agent) markReasoningSummaryBreak(itemID, threadID string, summaryIndex *int) {
	key := codexReasoningKey(threadID, itemID)
	a.Mu.Lock()
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
	a.Mu.Unlock()
}

// codexMethodThreadSettingsUpdated is the method of the frame the app server
// sends when a thread's own settings move. Go reads it and the browser never
// does -- the change reaches the browser through the shared settings pipeline --
// so the token stays here rather than in a contract.
const codexMethodThreadSettingsUpdated = contracts.CodexMethodThreadSettingsUpdated

// handleThreadSettingsUpdated folds the settings the thread settled on back into
// the agent.
//
// Codex changes a thread's own settings for reasons LeapMux never asked for: a
// `/model` typed into the composer, a collaboration mode a preset carries, an
// effort the model clamps. The picker went on showing what LeapMux last
// requested, which the running thread had already left.
//
// `PersistSettingsRefresh` is the same pipeline every other axis uses, and it is
// a no-op when nothing moved -- so a change LeapMux itself made, whose value the
// agent already holds, announces nothing.
func (a *Agent) handleThreadSettingsUpdated(params json.RawMessage) {
	var notif struct {
		ThreadSettings *struct {
			Model             string `json:"model"`
			Effort            string `json:"effort"`
			CollaborationMode string `json:"collaborationMode"`
		} `json:"threadSettings"`
	}
	if err := json.Unmarshal(params, &notif); err != nil || notif.ThreadSettings == nil {
		slog.Warn("codex thread/settings/updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	settings := notif.ThreadSettings
	a.Mu.Lock()
	// Each axis moves ONLY when the frame states it. An absent field is the app
	// server saying nothing about that axis, and writing "" would clear a value
	// the reader chose.
	if settings.Model != "" {
		a.model = settings.Model
	}
	if settings.Effort != "" {
		a.effort = settings.Effort
	}
	if settings.CollaborationMode != "" {
		a.collaborationMode = settings.CollaborationMode
	}
	vals := a.codexAxisValuesLocked()
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(vals)
}

// handleAgentMessageDelta counts and buffers item/agentMessage/delta.
func (a *Agent) handleAgentMessageDelta(params json.RawMessage) {
	if delta, ok := parseCodexModelDelta(params); ok {
		if delta.ItemID == "" {
			delta.ItemID = codexAssistantFallbackScope
		}
		a.bufferCodexModelText(delta.ItemID, delta.ThreadID, delta.Delta, agent.AssembledMessageKindText, providerkit.JoinVerbatim, true)
	}
}

// handlePlanDelta counts and buffers item/plan/delta.
func (a *Agent) handlePlanDelta(params json.RawMessage) {
	if delta, ok := parseCodexModelDelta(params); ok {
		if delta.ItemID == "" {
			delta.ItemID = codexPlanFallbackScope
		}
		a.bufferCodexModelText(delta.ItemID, delta.ThreadID, delta.Delta, agent.AssembledMessageKindPlan, providerkit.JoinVerbatim, true)
	}
}

func (a *Agent) handleReasoningSummaryTextDelta(params json.RawMessage) {
	if notif, ok := parseCodexModelDelta(params); ok && notif.ItemID != "" {
		a.observeReasoningText(notif.ItemID, codexReasoningKindSummary, notif.ThreadID, notif.Delta, notif.SummaryIndex)
	}
}

func (a *Agent) handleReasoningSummaryPartAdded(params json.RawMessage) {
	var notif struct {
		ItemID       string `json:"itemId"`
		ThreadID     string `json:"threadId"`
		SummaryIndex *int   `json:"summaryIndex"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" {
		a.markReasoningSummaryBreak(notif.ItemID, notif.ThreadID, notif.SummaryIndex)
	}
}

func (a *Agent) handleReasoningTextDelta(params json.RawMessage) {
	if notif, ok := parseCodexModelDelta(params); ok && notif.ItemID != "" {
		a.observeReasoningText(notif.ItemID, codexReasoningKindRaw, notif.ThreadID, notif.Delta, nil)
	}
}

func (a *Agent) handleCommandExecutionOutputDelta(params json.RawMessage) {
	a.handleCodexToolOutputDelta(params)
}

func (a *Agent) handleCommandExecutionTerminalInteraction(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Stdin  string `json:"stdin"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Stdin != "" {
		a.Mu.Lock()
		a.appendCodexToolEventLocked(notif.ItemID, notif.Stdin, true)
		a.Mu.Unlock()
	}
}

func (a *Agent) handleFileChangeOutputDelta(params json.RawMessage) {
	a.handleCodexToolOutputDelta(params)
}

func (a *Agent) handleCodexToolOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.appendCodexToolOutput(notif.ItemID, notif.Delta)
		// The ACCUMULATED output, not the delta that just landed: Codex sends
		// deltas, and only this agent knows where the earlier ones ended. It is
		// empty for a delta that arrived before its item did, and an empty tail
		// says nothing a reader can use.
		tail, truncated := a.codexToolOutputSoFar(notif.ItemID)
		reportOutput := func(sink agent.ProviderServices) {
			sink.ReportProgress(agent.OutputDeltaProgress(notif.ItemID, int64(len([]byte(notif.Delta)))))
			if tail != "" {
				sink.ReportProgress(agent.OutputTailProgress(notif.ItemID, tail, truncated))
			}
		}
		if childSink, childOwned := a.childSinkForItem(notif.ItemID); childOwned {
			if childSink != nil {
				reportOutput(childSink)
			}
			return
		}
		reportOutput(a.sink)
	}
}
