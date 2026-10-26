package muse

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type pendingMessage struct {
	content  agent.MessageContent
	source   leapmuxv1.MessageSource
	span     agent.SpanInfo
	item     *itemState
	revision int64
}

// turnFinalization keeps the original completion and the progress of each write.
// finalizeMu protects writes. stateMu protects publication and session ownership.
type turnFinalization struct {
	turnID   string
	messages []pendingMessage
	retries  []agent.CapturedTranscript
	end      agent.MessageContent
}

func nativeItemKey(id string, revision int64) string {
	return fmt.Sprintf("muse:item:%s:%d", id, revision)
}

func (a *Agent) captureFinalization(state *sessionState, id, turnID string, raw []byte, ended agent.MessageCompletion) *turnFinalization {
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
	if state.completed[turnID] {
		a.stateMu.Unlock()
		return nil
	}
	state.completed[turnID] = true
	var items []*itemState
	for _, item := range state.items {
		if item.params.Item.TurnID != nil && *item.params.Item.TurnID == turnID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].order < items[j].order })
	pending := &turnFinalization{turnID: turnID, retries: state.retryNotices}
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
			if item.persistedRevision >= params.Item.Revision || params.Item.Kind == contracts.MuseItemKindCompaction {
				continue
			}
		} else if !tool {
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
		message := pendingMessage{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, item: item, revision: params.Item.Revision,
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

// museWriteIsRetained reports that the shared queue owns the failed write and will retry it.
func museWriteIsRetained(err error) bool {
	var retained *agent.PendingOutputError
	return errors.As(err, &retained)
}

// retryFinalizations submits each complete turn before the caller can release its queue.
// A retained failure stays in the shared queue. An unretained failure stops submission.
func (a *Agent) retryFinalizations(state *sessionState) {
	a.finalizeMu.Lock()
	defer a.finalizeMu.Unlock()
	a.stateMu.Lock()
	pending := slices.Clone(state.finalizations)
	a.stateMu.Unlock()
	for _, turn := range pending {
		for len(turn.messages) > 0 {
			message := turn.messages[0]
			err := state.sink.PersistMessage(message.source, message.content, message.span)
			if err != nil {
				slog.Warn("persist captured Muse output", "turn", turn.turnID, "error", err)
				if !museWriteIsRetained(err) {
					return
				}
			}
			if message.item != nil && err == nil {
				a.stateMu.Lock()
				message.item.persistedRevision = max(message.item.persistedRevision, message.revision)
				a.stateMu.Unlock()
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
				if !museWriteIsRetained(err) {
					return
				}
			}
			turn.retries = turn.retries[1:]
		}
		if err := state.sink.PersistTurnEnd(turn.end, agent.SpanInfo{}); err != nil {
			slog.Warn("persist the captured Muse turn end", "turn", turn.turnID, "error", err)
			if !museWriteIsRetained(err) {
				return
			}
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
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		a.stateMu.Lock()
		states := make(map[string]*sessionState, len(a.sessions))
		for id, state := range a.sessions {
			states[id] = state
			state.retired = true
		}
		a.stateMu.Unlock()
		for id, state := range states {
			a.retireControlPublications(state)
			a.endSessionCompactions(state)
			a.stateMu.Lock()
			turnID := state.turnID
			controls := state.controls
			state.controls = nil
			a.stateMu.Unlock()
			if turnID != "" {
				a.captureFinalization(state, id, turnID, nil, ended)
			}
			a.retryFinalizations(state)
			for _, control := range controls {
				if control.published {
					a.enqueueControl(&controlRecord{kind: controlCancel, state: state, key: control.key})
				}
			}
			if state.childKey != "" {
				if err := a.sink.CloseBackgroundTask(state.childKey, bgtask.StatusStopped); err != nil {
					slog.Warn("close the Muse child task", "error", err)
				}
			}
			if turnID != "" {
				a.Mu.Lock()
				seq := a.NextTurnSeq()
				a.Mu.Unlock()
				state.sink.SetTurnState(agent.TurnState{}, seq)
			}
		}
	}()
	a.drainControls()
}
