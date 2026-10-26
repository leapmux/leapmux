package muse

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	func() {
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		a.dispatchOutput(line)
	}()
	// Goal writes run after dispatchMu releases.
	// A native goal observer can call HandleOutput from its UpsertGoal or ClearGoal callback.
	// The callback must hold no provider or lifecycle lock, because it can enter the same dispatcher.
	for _, apply := range a.takeDeferredGoalActions() {
		apply()
	}
	a.drainControls()
	if line.Method == methodSessionApprovalChanged {
		a.queuePermissionApprovals()
	}
	// A fresh native child can expose a live head before its stored view prefix.
	// Retry that prefix on received child events. Replayed frames use
	// dispatchOutput directly, so they cannot schedule their own reads.
	if viewCursor(line.Raw) != "" {
		id := providerkit.StringField(line.Params, "sessionId")
		a.stateMu.Lock()
		state := a.sessions[id]
		needsPrefix := a.currentSessionState(id, state) && state.childID != "" && state.childViewPrefix == childViewPrefixPending
		a.stateMu.Unlock()
		if needsPrefix {
			a.requestChildView(id)
		}
	}
}

func (a *Agent) dispatchOutput(line *providerkit.ParsedLine) {
	if line.HasID() {
		if line.Method == contracts.MuseMethodApprovalRequest || line.Method == contracts.MuseMethodUserInputRequest {
			a.SendResponseDetached(line.ID, map[string]any{}, "acknowledge a Muse control request")
			a.publishControl(line)
		} else {
			a.RefuseUnsupportedRequest(line)
		}
		return
	}
	if line.Method == contracts.MuseMethodLogRecord {
		a.handleLogRecord(line.Params)
		return
	}
	if line.Method == methodLogGap {
		a.handleLogGap(line.Params)
		return
	}
	var identity struct {
		SessionID string `json:"sessionId"`
		TurnID    string `json:"turnId"`
	}
	if json.Unmarshal(line.Params, &identity) != nil {
		return
	}
	a.stateMu.Lock()
	state := a.sessions[identity.SessionID]
	a.stateMu.Unlock()
	if state == nil {
		if line.Method == contracts.MuseMethodUsageChanged {
			a.handleUsage(line.Params, a.sink)
		} else {
			slog.Debug("ignore a Muse event without an owned session", "method", line.Method, "session", identity.SessionID)
		}
		return
	}
	if a.bufferViewFrame(line, state) {
		return
	}
	if state.childID != "" {
		if cursor := viewCursor(line.Raw); cursor != "" {
			a.stateMu.Lock()
			state.lastViewCursor = cursor
			a.stateMu.Unlock()
		}
	}
	switch line.Method {
	case methodTurnStarted:
		a.startTurn(identity.SessionID, identity.TurnID)
	case contracts.MuseMethodTurnCompleted:
		a.finishTurn(line.Raw, identity.SessionID, identity.TurnID)
	case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
		a.handleItem(line.Raw, line.Params, state)
	case contracts.MuseMethodItemDelta:
		a.handleDelta(line.Params, state)
	case contracts.MuseMethodApprovalRequested, contracts.MuseMethodApprovalUpdated, contracts.MuseMethodUserInputRequested:
		a.publishControl(line)
	case contracts.MuseMethodApprovalResolved, contracts.MuseMethodUserInputSettled:
		a.settleControl(line, state)
	default:
		a.handleEvent(line, state)
	}
}

func (a *Agent) startTurn(sessionID, turnID string) {
	if turnID == "" {
		return
	}
	a.stateMu.Lock()
	state := a.sessions[sessionID]
	if state == nil || state.retired || state.completed[turnID] || state.turnID == turnID {
		a.stateMu.Unlock()
		return
	}
	state.turnID = turnID
	state.toolUses = 0
	root := sessionID == a.sessionID
	retiredRoot := !root && state.childID == ""
	a.stateMu.Unlock()
	a.retryFinalizations(state)
	if root {
		a.PublishTurnActive()
	} else if !retiredRoot {
		a.Mu.Lock()
		seq := a.NextTurnSeq()
		a.Mu.Unlock()
		state.sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, seq)
	}
}

func (a *Agent) handleItem(raw, params []byte, state *sessionState) {
	var item itemParams
	if json.Unmarshal(params, &item) != nil || item.Item.ID == "" || item.Item.Revision <= 0 {
		return
	}
	a.stateMu.Lock()
	existing := state.items[item.Item.ID]
	var previous itemParams
	if existing != nil {
		previous = existing.params
		currentRevision := existing.params.Item.Revision
		if item.Item.Revision < currentRevision || item.Item.Revision == currentRevision && (existing.persistedRevision >= currentRevision || !bytes.Equal(existing.raw, raw)) {
			a.stateMu.Unlock()
			return
		}
	}
	if existing == nil {
		existing = &itemState{deltas: make(map[string]string), cursors: make(map[string]bool), order: state.nextItemOrder}
		state.nextItemOrder++
		state.items[item.Item.ID] = existing
	}
	if item.Item.Revision > existing.params.Item.Revision {
		existing.params = item
		existing.raw = slices.Clone(raw)
	}
	state.ensureNativeItemIndex().replace(previous, existing)
	tool := item.Item.Kind == contracts.MuseItemKindToolCall || item.Item.Kind == contracts.MuseItemKindUserShell
	open := tool && !existing.opened
	if open {
		existing.opened = true
		state.toolUses++
	}
	finished := item.Item.Status != contracts.MuseItemStatusInProgress
	current := a.currentSessionState(item.SessionID, state)
	modelTool := open && item.Item.Kind == contracts.MuseItemKindToolCall && current
	a.stateMu.Unlock()
	if modelTool && a.flushRetryNotices(state) {
		state.retry.Resolve(state.sink)
	}
	if tool {
		if open && !finished {
			if a.persistItem(state, existing, false) && current {
				state.sink.OpenSpan(item.Item.ID, "")
				state.sink.SetSpanType(item.Item.ID, item.Item.Tool)
			}
		}
		if finished {
			a.persistItem(state, existing, true)
			if current {
				state.sink.CloseSpan(item.Item.ID)
				state.sink.ReportProgress(agent.CompleteOutputProgress(item.Item.ID))
			}
		}
		a.enrichToolGroup(state, existing, previous)
	} else if item.Item.Kind == contracts.MuseItemKindCompaction {
		a.stateMu.Lock()
		publish := current && !finished && !state.compactionLive[item.Item.ID]
		if publish {
			if state.compactionLive == nil {
				state.compactionLive = make(map[string]bool)
			}
			state.compactionLive[item.Item.ID] = true
		}
		if finished {
			delete(state.compactionLive, item.Item.ID)
		}
		a.stateMu.Unlock()
		if publish {
			providerkit.PublishCompaction(state.sink, item.Item.Trigger)
		}
		if finished {
			if current {
				providerkit.EndCompaction(state.sink)
			}
			_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, museItemMessageContent(slices.Clone(raw), item))
			if err != nil {
				slog.Warn("persist Muse compaction", "error", err)
			} else {
				a.stateMu.Lock()
				existing.persistedRevision = max(existing.persistedRevision, item.Item.Revision)
				a.stateMu.Unlock()
			}
			// A compaction runs outside model turns. The host reports no idle state for it.
			// End the worker's compaction turn here.
			a.stateMu.Lock()
			root := item.SessionID == a.sessionID
			a.stateMu.Unlock()
			if root {
				a.PublishTurnActive()
			}
		}
	} else if finished && !museSilentItem(item.Item) {
		a.persistItem(state, existing, false)
	}
	if item.Item.Kind == contracts.MuseItemKindAgentMessage || item.Item.Kind == contracts.MuseItemKindReasoning {
		if finished && existing.persistedRevision >= item.Item.Revision {
			state.generation.Discard(item.Item.ID)
			for _, scope := range existing.generationScopes {
				state.generation.Discard(scope)
			}
		}
	}
	if finished {
		a.flushRetryNotices(state)
	}
	if current && (item.Item.Kind == museItemSubagent || item.Item.Kind == contracts.MuseItemKindWorkflow || item.Item.Background) {
		if err := a.observeChildOrTask(item, state, previous.Item); err != nil {
			slog.Warn("publish Muse child metadata", "error", err)
		}
	}
}

// endSessionCompactions withdraws live state when the session retires.
func (a *Agent) endSessionCompactions(state *sessionState) {
	if state == nil {
		return
	}
	a.stateMu.Lock()
	live := len(state.compactionLive) > 0
	state.compactionLive = nil
	a.stateMu.Unlock()
	if live {
		providerkit.EndCompaction(state.sink)
	}
}

// museSilentItem identifies native items that no transcript reader displays.
func museSilentItem(item nativeItem) bool {
	// The worker owns root and child user rows. Spawn metadata supplies the
	// initial child prompt; the input queue supplies later human messages.
	return item.Kind == contracts.MuseItemKindUserMessage || item.Kind == museItemSubagent || item.Kind == museItemReminderChild ||
		item.Kind == contracts.MuseItemKindHookRun && item.Status == contracts.MuseItemStatusCompleted
}

func (a *Agent) persistItem(state *sessionState, item *itemState, closing bool) bool {
	a.stateMu.Lock()
	revision := item.params.Item.Revision
	if item.persistedRevision >= revision {
		a.stateMu.Unlock()
		return true
	}
	raw := slices.Clone(item.raw)
	params := item.params
	a.stateMu.Unlock()
	span := agent.SpanInfo{}
	if params.Item.Kind == contracts.MuseItemKindToolCall || params.Item.Kind == contracts.MuseItemKindUserShell {
		span = agent.SpanInfo{SpanID: params.Item.ID, SpanType: params.Item.Tool, Closing: closing}
	}
	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	content := museItemMessageContent(raw, params)
	if err := state.sink.PersistMessage(source, content, span); err != nil {
		slog.Warn("persist a Muse item", "item", params.Item.ID, "error", err)
		return museWriteIsRetained(err)
	}
	a.stateMu.Lock()
	item.persistedRevision = max(item.persistedRevision, revision)
	a.stateMu.Unlock()
	return true
}

// museItemCompletion preserves the native failure state of a finished text item.
func museItemCompletion(status string) agent.MessageCompletion {
	switch status {
	case contracts.MuseItemStatusFailed, contracts.MuseItemStatusTimedOut:
		return agent.MessageCompletionError
	case contracts.MuseItemStatusCancelled:
		return agent.MessageCompletionInterrupted
	default:
		return ""
	}
}

// museItemMessageContent gives normal and retried writes the same native completion.
// The caller supplies an owned copy of raw.
func museItemMessageContent(raw []byte, params itemParams) agent.MessageContent {
	content := agent.MessageContent{Original: raw, AgentSessionID: params.SessionID, IdempotencyKey: nativeItemKey(params.Item.ID, params.Item.Revision)}
	if params.Item.Kind == contracts.MuseItemKindAgentMessage || params.Item.Kind == contracts.MuseItemKindReasoning {
		content.Completion = museItemCompletion(params.Item.Status)
	}
	return content
}

func (a *Agent) handleDelta(raw []byte, state *sessionState) {
	var delta struct {
		SessionID string `json:"sessionId"`
		ItemID    string `json:"itemId"`
		Field     string `json:"field"`
		Delta     string `json:"delta"`
		Cursor    string `json:"viewCursor"`
	}
	if json.Unmarshal(raw, &delta) != nil || delta.ItemID == "" || delta.Cursor == "" {
		return
	}
	a.stateMu.Lock()
	item := state.items[delta.ItemID]
	if item == nil || item.params.Item.Status != contracts.MuseItemStatusInProgress || item.cursors[delta.Cursor] {
		a.stateMu.Unlock()
		return
	}
	item.cursors[delta.Cursor] = true
	field := delta.Field
	if field == "" {
		field = "text"
	}
	if !validDeltaField(item.params.Item.Kind, field) {
		a.stateMu.Unlock()
		return
	}
	item.deltas[field] += delta.Delta
	kind := item.params.Item.Kind
	output := item.deltas[field]
	scope := delta.ItemID
	if kind == contracts.MuseItemKindReasoning {
		if field != item.lastGenerationField || len(item.generationScopes) == 0 {
			item.generationPart++
			scope = delta.ItemID + ":summary:" + strconv.FormatUint(item.generationPart, 10)
			item.generationScopes = append(item.generationScopes, scope)
			item.lastGenerationField = field
		} else {
			scope = item.generationScopes[len(item.generationScopes)-1]
		}
	}
	retired := state.retired
	a.stateMu.Unlock()
	switch kind {
	case contracts.MuseItemKindAgentMessage, contracts.MuseItemKindReasoning:
		if !retired && delta.Delta != "" && a.flushRetryNotices(state) {
			state.retry.Resolve(state.sink)
		}
		assembled := agent.AssembledMessageKindText
		if kind == contracts.MuseItemKindReasoning {
			assembled = agent.AssembledMessageKindReasoning
		}
		state.generation.Append(scope, assembled, delta.Delta, providerkit.JoinVerbatim)
		if !retired {
			state.sink.ReportProgress(agent.ModelTextProgress(delta.ItemID, delta.Delta))
		}
	case contracts.MuseItemKindToolCall, contracts.MuseItemKindUserShell:
		if field == "output" && !retired {
			state.sink.ReportProgress(agent.OutputDeltaProgress(delta.ItemID, int64(len(delta.Delta))))
			state.sink.ReportProgress(agent.OutputTailProgress(delta.ItemID, output, false))
		}
	}
}

func validDeltaField(kind, field string) bool {
	switch kind {
	case contracts.MuseItemKindAgentMessage:
		return field == "text"
	case contracts.MuseItemKindReasoning:
		if !strings.HasPrefix(field, "summary.") {
			return false
		}
		index := strings.TrimPrefix(field, "summary.")
		value, err := strconv.ParseUint(index, 10, 31)
		return err == nil && strconv.FormatUint(value, 10) == index
	case contracts.MuseItemKindToolCall, contracts.MuseItemKindUserShell:
		return field == "output"
	default:
		return false
	}
}

func completion(outcome string) agent.MessageCompletion {
	switch outcome {
	case contracts.MuseTurnOutcomeCompleted:
		return agent.MessageCompletionComplete
	case contracts.MuseTurnOutcomeCancelled:
		return agent.MessageCompletionInterrupted
	case contracts.MuseTurnOutcomeFailed:
		return agent.MessageCompletionError
	default:
		return agent.MessageCompletionFinished
	}
}
func (a *Agent) finishTurn(raw []byte, id, turnID string) {
	var frame struct {
		Params turnCompletion `json:"params"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Params.SessionID != id || frame.Params.TurnID != turnID || frame.Params.Outcome == "" {
		return
	}
	a.stateMu.Lock()
	state := a.sessions[id]
	if state == nil || turnID == "" || !state.completed[turnID] && state.turnID != turnID {
		a.stateMu.Unlock()
		return
	}
	root := id == a.sessionID
	retiredRoot := !root && state.childID == ""
	a.stateMu.Unlock()
	state.retry.Reset()
	if a.captureFinalization(state, id, turnID, raw, completion(frame.Params.Outcome)) == nil {
		return
	}
	a.retryFinalizations(state)
	if root {
		a.PublishTurnActive()
	} else if !retiredRoot {
		a.Mu.Lock()
		seq := a.NextTurnSeq()
		a.Mu.Unlock()
		state.sink.SetTurnState(agent.TurnState{}, seq)
	}
}
