package muse

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type pendingMessage struct {
	content  agent.MessageContent
	source   leapmuxv1.MessageSource
	span     agent.SpanInfo
	item     *itemState
	revision int64
	params   itemParams
}

// turnFinalization keeps the original completion and the progress of each write.
// dispatcher.finalizationMu protects writes. stateMu protects publication and session ownership.
type turnFinalization struct {
	turnID   string
	messages []pendingMessage
	retries  []agent.CapturedTranscript
	end      agent.MessageContent
	// verdict is the retry verdict of a root turn, decided once when the
	// finalization is captured. retryFinalizations applies it right after the
	// turn-end row, so a replayed completion can neither decide again nor apply
	// it before that row. A root turn that a host retirement ends takes
	// NotRetryable, as every other turn end does. Nil applies none: a child
	// turn.
	verdict *providerkit.TurnFailure
}

func nativeItemKey(id string, revision int64) string {
	return fmt.Sprintf("muse:item:%s:%d", id, revision)
}

// captureFinalization captures the output of a turn that ends, once per turn.
// verdict is the retry verdict of a root turn, and nil for any other. A replay
// of the same completion returns the finalization that the first one captured,
// with its first verdict.
func (a *Agent) captureFinalization(state *sessionState, id, turnID string, raw []byte, ended agent.MessageCompletion, verdict *providerkit.TurnFailure) *turnFinalization {
	a.stateMu.Lock()
	for _, pending := range state.finalizations {
		if pending.turnID == turnID {
			a.stateMu.Unlock()
			if bytes.Equal(pending.end.Original, raw) {
				return pending
			}
			return nil
		}
	}
	if state.completed[turnID] == turnCompletionCaptured {
		a.stateMu.Unlock()
		return nil
	}
	if state.completed == nil {
		state.completed = make(map[string]turnCompletionState)
	}
	state.completed[turnID] = turnCompletionCaptured
	var items []*itemState
	for _, item := range state.items {
		if len(item.raw) > 0 && item.params.Item.TurnID != nil && *item.params.Item.TurnID == turnID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].order < items[j].order })
	pending := &turnFinalization{turnID: turnID, retries: state.retryNotices, verdict: verdict}
	state.retryNotices = nil
	state.retryItems = nil
	for _, item := range items {
		params := item.params
		if museSilentItem(params.Item) {
			continue
		}
		tool := params.Item.Kind == contracts.MuseItemKindToolCall || params.Item.Kind == contracts.MuseItemKindUserShell
		finished := params.Item.Status != contracts.MuseItemStatusInProgress
		if finished {
			state.generation.Discard(params.Item.ID)
			for _, scope := range item.generationScopes {
				state.generation.Discard(scope)
			}
			if item.retainedRevision >= params.Item.Revision || params.Item.Kind == contracts.MuseItemKindCompaction {
				continue
			}
		} else if !tool {
			// Child text reaches this state only after its native source is proved.
			// A full in-progress revision can replace deltas before an interruption.
			// Keep that original revision when no finished native item supersedes it.
			if state.childID != "" && params.SourceRange.Stream == (nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}) && params.SourceRange.Last.ID != "" && params.SourceRange.Last.Sequence > 0 {
				content := museItemMessageContent(slices.Clone(item.raw), params)
				content.Completion = ended
				content.IdempotencyKey = "muse:retained:" + turnID + ":" + params.Item.ID
				pending.messages = append(pending.messages, pendingMessage{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: content, item: item, revision: params.Item.Revision, params: cloneItemParams(params)})
				state.generation.Discard(params.Item.ID)
				for _, scope := range item.generationScopes {
					state.generation.Discard(scope)
				}
				continue
			}
			scopes := append([]string{params.Item.ID}, item.generationScopes...)
			for _, scope := range scopes {
				_, err := state.generation.PersistScope(scope, ended, func(content []byte) error {
					pending.messages = append(pending.messages, pendingMessage{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
						content: agent.MessageContent{Original: content, AgentSessionID: id, Completion: ended, IdempotencyKey: "muse:partial:" + turnID + ":" + scope}})
					return nil
				})
				if err != nil {
					slog.Error("capture Muse partial text", "error", err)
				}
			}
			continue
		} else if !item.opened {
			continue
		}
		message := pendingMessage{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, item: item, revision: params.Item.Revision, params: cloneItemParams(params),
			content: museItemMessageContent(slices.Clone(item.raw), params)}
		if tool {
			message.span = agent.SpanInfo{SpanID: params.Item.ID, SpanType: params.Item.Tool, Closing: true}
		}
		if !finished {
			message.content.Completion = ended
			message.content.IdempotencyKey = "muse:retained:" + turnID + ":" + params.Item.ID
		}
		pending.messages = append(pending.messages, message)
	}
	partials, err := state.generation.FinishAll(ended)
	if err != nil {
		slog.Error("capture Muse partial text", "error", err)
	}
	for index, content := range partials {
		pending.messages = append(pending.messages, pendingMessage{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			content: agent.MessageContent{Original: content, AgentSessionID: id, Completion: ended, IdempotencyKey: fmt.Sprintf("muse:partial:%s:%d", turnID, index)}})
	}
	pending.end = agent.WithToolUseCount(agent.MessageContent{Original: slices.Clone(raw), AgentSessionID: id, Completion: ended, IdempotencyKey: "muse:turn:" + turnID}, int(state.toolUses))
	for i := range pending.messages {
		pending.messages[i].content.WriteReceipt = agent.NewTranscriptWriteReceipt()
	}
	pending.end.WriteReceipt = agent.NewTranscriptWriteReceipt()
	state.finalizations = append(state.finalizations, pending)
	if state.turnID == turnID {
		state.turnID = ""
	}
	a.stateMu.Unlock()
	return pending
}

// retryFinalizations submits each complete turn before the caller can release its queue.
// A retained failure stays in the shared queue. An unretained failure stops submission.
func (a *Agent) retryFinalizations(state *sessionState) {
	a.dispatcher.finalizationMu.Lock()
	defer a.dispatcher.finalizationMu.Unlock()
	a.stateMu.Lock()
	pending := slices.Clone(state.finalizations)
	a.stateMu.Unlock()
	for _, turn := range pending {
		for len(turn.messages) > 0 {
			message := turn.messages[0]
			err := state.sink.PersistMessage(message.source, message.content, message.span)
			if err != nil {
				slog.Warn("persist captured Muse output", "turn", turn.turnID, "error", err)
				if !agent.OutputRetained(err) {
					return
				}
			}
			if message.item != nil {
				a.stateMu.Lock()
				message.item.retainedRevision = max(message.item.retainedRevision, message.revision)
				var publication *nativeToolPublication
				if owner, session, known := message.content.WriteReceipt.CapturedPublication(); known {
					publication = &nativeToolPublication{publication: owner, sessionID: session}
				}
				if message.params.Item.Kind == contracts.MuseItemKindToolCall || message.params.Item.Kind == contracts.MuseItemKindUserShell {
					if message.item.params.Item.Revision == message.revision {
						message.item.retainedPublication = publication
					}
					observation := state.freezeToolObservation(message.params, message.content.Original, message.content.WriteReceipt, publication)
					a.stateMu.Unlock()
					if observation != nil {
						a.enrichTool(*observation)
					}
				} else {
					a.stateMu.Unlock()
				}
			}
			if message.span.Closing {
				state.sink.CloseSpan(message.span.SpanID)
				state.sink.ReportProgress(agent.CompleteOutputProgress(message.span.SpanID))
			}
			turn.messages[0] = pendingMessage{}
			turn.messages = turn.messages[1:]
		}
		for len(turn.retries) > 0 {
			if _, err := turn.retries[0].PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
				slog.Error("persist a captured Muse retry notice", "error", err)
				if !agent.OutputRetained(err) {
					return
				}
			}
			turn.retries = turn.retries[1:]
		}
		if err := state.sink.PersistTurnEnd(turn.end, agent.SpanInfo{}); err != nil {
			slog.Warn("persist the captured Muse turn end", "turn", turn.turnID, "error", err)
			if !agent.OutputRetained(err) {
				return
			}
		}
		// The turn-end row is written or retained in the shared queue, so the
		// retry operations follow it. The finalization leaves the list below, so
		// the verdict applies once.
		if turn.verdict != nil {
			providerkit.ApplyTurnFailure(state.sink, *turn.verdict, turn.end.Original)
		}
		a.stateMu.Lock()
		state.finalizations[0] = nil
		state.finalizations = state.finalizations[1:]
		a.stateMu.Unlock()
	}
}

// retireHost retains provider-owned output before it withdraws live session state.
func (a *Agent) retireHost(ended agent.MessageCompletion) {
	func() {
		a.dispatcher.mu.Lock()
		defer a.dispatcher.mu.Unlock()
		a.stateMu.Lock()
		states := make(map[string]*sessionState, len(a.sessions))
		for id, state := range a.sessions {
			states[id] = state
			state.retired = true
			if state.projection != nil {
				state.projection.signal()
			}
		}
		rootID := a.sessionID
		a.stateMu.Unlock()
		a.retireSessionStates(states, rootID, ended)
	}()
	a.drainDeferredOutputActions()
	a.drainControls()
}

// retireSessionStates preserves every captured output before closing any child
// registry row. The caller holds dispatcher.mu and has retired all supplied states.
// rootID identifies the root session, whose open turn ends with no retry.
func (a *Agent) retireSessionStates(states map[string]*sessionState, rootID string, ended agent.MessageCompletion) {
	for id, state := range states {
		a.stateMu.Lock()
		var bootstrap [][]byte
		if state.viewRecovery != nil {
			bootstrap = state.viewRecovery.buffer
			state.viewRecovery.buffer = nil
			state.viewRecovery.bufferedBytes = 0
			state.viewRecovery = nil
		}
		a.stateMu.Unlock()
		if len(bootstrap) > 0 {
			writer, sessionID, frames := a.sink, id, bootstrap
			a.deferOutputAction(func() { persistUnprojectedViewFrames(writer, sessionID, frames) })
		}
		a.stateMu.Lock()
		pending := state.projection != nil && (len(state.projection.frames) > 0 || len(state.projection.heads) > 0)
		a.stateMu.Unlock()
		if pending {
			a.failChildProjectionLocked(id, state, agent.ErrChildRouteNotReady, nil)
		}
		a.retireControlPublications(state)
		a.endSessionCompactions(state)
		if id == rootID {
			// The retirement ends a manual compaction that still runs.
			a.endManualCompaction(id, state.sink)
		}
		a.stateMu.Lock()
		turnID := state.turnID
		controls := state.controls
		state.controls = nil
		a.stateMu.Unlock()
		if turnID != "" {
			// The root's turn ends without a native end, so no retry follows it.
			var verdict *providerkit.TurnFailure
			if id == rootID {
				verdict = &providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
			}
			a.captureFinalization(state, id, turnID, nil, ended, verdict)
		}
		a.retryFinalizations(state)
		for _, control := range controls {
			if control.published {
				a.enqueueControl(&controlRecord{kind: controlCancel, state: state, key: control.key})
			}
		}
		if turnID != "" {
			a.Mu.Lock()
			seq := a.NextTurnSeq()
			a.Mu.Unlock()
			state.sink.SetTurnState(agent.TurnState{}, seq)
		}
	}
	a.stateMu.Lock()
	keys := make(map[string]bool)
	for id, state := range states {
		for _, key := range sessionRegistryRowKeys(id, state) {
			keys[key] = true
		}
	}
	a.stateMu.Unlock()
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.Sort(ordered)
	rows := make([]nativeRegistryRow, 0, len(ordered))
	for _, key := range ordered {
		rows = append(rows, nativeRegistryRow{closeKey: key})
	}
	if len(rows) > 0 {
		if err := a.queueRegistryBatchLocked(&nativeRegistryBatch{writer: a.sink, rows: rows}); err != nil {
			slog.Error("retain Muse registry retirement", "error", err)
		}
	}
}
