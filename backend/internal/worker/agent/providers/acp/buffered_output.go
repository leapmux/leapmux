package acp

import (
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// queueTurnOutputCleanup keeps the captured provider cleanup after its output.
func (b *Base) queueTurnOutputCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	b.queueOutputCallback("clear completed ACP output state", "complete ACP output before state cleanup", func() error {
		cleanup()
		return nil
	})
}

func (b *Base) queueReplyReconciliation(reconcile func() error) {
	if reconcile == nil {
		return
	}
	b.queueOutputCallback("reconcile ACP reply ownership", "reconcile ACP reply ownership", reconcile)
}

func (b *Base) queueOutputCallback(label, errorLog string, run func() error) {
	queue := agent.OutputQueueFor(b.sink)
	if queue == nil {
		if err := run(); err != nil {
			slog.Error(errorLog, "agent_id", b.AgentID(), "error", err)
		}
		return
	}
	queue.Append(agent.OutputWrite{Label: label, Run: run})
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) && !agent.OutputRetained(err) {
		slog.Error(errorLog, "agent_id", b.AgentID(), "error", err)
	}
}

// FlushBufferedOutput stores open reasoning, then text, with the requested completion.
// Call it only from the reader. Other goroutines use FlushBufferedOutputOffReader.
func (b *Base) FlushBufferedOutput(completion agent.MessageCompletion) {
	if b.IsDiscardingOutput() {
		b.DiscardBufferedOutput()
		return
	}
	b.turnMu.Lock()
	turn := b.takeBufferedTextLocked()
	b.turnMu.Unlock()
	b.main().persistBufferedText(turn, completion)
}

// FlushBufferedOutputOffReader serializes a flush with session-update dispatch.
// Never call it from the reader, which can already hold sessionUpdates.mu.
func (b *Base) FlushBufferedOutputOffReader(completion agent.MessageCompletion) {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	b.FlushBufferedOutput(completion)
}

// FlushNativeReplyOutput stores buffered output only if it belongs to the
// native messages whose reply has ended. An overlapping prompt can already
// have started streaming, so its buffer must keep its original lifetime.
func (b *Base) FlushNativeReplyOutput(sessionID string, messageIDs map[string]bool) {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	b.turnMu.Lock()
	b.Mu.Lock()
	current := b.sessionID == sessionID
	b.Mu.Unlock()
	if !current || b.IsDiscardingOutput() {
		b.turnMu.Unlock()
		return
	}
	turn := b.takeNativeReplyTextLocked(messageIDs)
	b.turnMu.Unlock()
	b.main().persistBufferedText(turn, agent.MessageCompletionComplete)
}

// FlushChildNativeReplyOutput stores matched text from one still-served child.
// A child that closed already drained its accepted text with its original IDs.
func (b *Base) FlushChildNativeReplyOutput(target ChildSessionTarget, messageIDs map[string]bool) {
	if !target.valid() || !b.IsCurrentSession(target.RootSessionID) || b.childSessionRow(target.SessionID) != target.RowKey {
		return
	}
	child := b.childFor(target.RowKey)
	if child == nil {
		return
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if !b.IsCurrentSession(target.RootSessionID) || !b.childRouteCurrent(target.RowKey, child) || child.childSink.MessageSessionID() != target.SessionID {
		return
	}
	child.out.turnMu.Lock()
	turn := child.out.takeNativeReplyTextLocked(messageIDs)
	child.out.turnMu.Unlock()
	child.persistBufferedText(turn, agent.MessageCompletionComplete)
}

func (out *acpTurnOutput) takeNativeReplyTextLocked(messageIDs map[string]bool) acpTurnSnapshot {
	if out.turnAssistantText.Len() > 0 && !messageIDs[out.assistantMessageID] ||
		out.turnThoughtText.Len() > 0 && !messageIDs[out.thoughtMessageID] {
		return acpTurnSnapshot{}
	}
	return out.takeBufferedTextLocked()
}

// DiscardBufferedOutput drops selected open segments and resets their live progress.
// With no kinds, it drops reasoning and text. Call it from the reader or under sessionUpdates.mu.
func (b *Base) DiscardBufferedOutput(kinds ...agent.AssembledMessageKind) {
	if len(kinds) == 0 {
		kinds = []agent.AssembledMessageKind{agent.AssembledMessageKindReasoning, agent.AssembledMessageKindText}
	}
	for _, kind := range kinds {
		switch kind {
		case agent.AssembledMessageKindReasoning, agent.AssembledMessageKindText:
			text, _ := b.takeText(kind)
			if text != "" {
				b.sink.ReportProgress(agent.ResetModelScopeProgress("acp:" + acpTextProgressScope(kind)))
			}
		default:
			slog.Warn("ACP has no text buffer for this message kind", "kind", kind)
		}
	}
}

// persistBufferedText writes nonempty segments without completing a scope that holds no text.
func (c *conversation) persistBufferedText(turn acpTurnSnapshot, completion agent.MessageCompletion) {
	persist := c.persistFinishedTextTo
	if turn.textProgressCompleted {
		persist = c.persistSelectedTextTo
	}
	if turn.thoughtText != "" && (turn.thoughtAdmission.captured() || (turn.textBatch == nil && !turn.textReservationFailed)) {
		persist(turn.thoughtAdmission, agent.AssembledMessageKindReasoning, turn.thoughtMessageID, "", false, turn.thoughtText, completion)
	}
	if turn.assistantText != "" && (turn.assistantAdmission.captured() || (turn.textBatch == nil && !turn.textReservationFailed)) {
		persist(turn.assistantAdmission, agent.AssembledMessageKindText, turn.assistantMessageID, turn.assistantTextKey, turn.assistantTextKeyResolved, turn.assistantText, completion)
	}
	if turn.textBatch == nil {
		return
	}
	// Root transcript admissions already reserved their own positions. This
	// batch holds the remaining text until preparation can run off the reader.
	err := turn.textBatch.Prepare(func() []agent.CapturedTranscriptOperation {
		const source = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		var operations []agent.CapturedTranscriptOperation
		appendText := func(kind agent.AssembledMessageKind, messageID, key string, keyResolved bool, text string, admission *acpTranscriptAdmission) {
			if text == "" || admission.captured() {
				return
			}
			template, prepare, err := c.preparedAssembledText(kind, messageID, key, keyResolved, text, completion)
			if err != nil {
				slog.Error("prepare buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
				return
			}
			template.AgentSessionID = turn.sessionID
			template = admission.tagReply(template)
			capture, present, err := agent.CapturePreparedTranscript(c.sink(), source, template, agent.SpanInfo{}, prepare)
			if err != nil {
				slog.Error("capture buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
				return
			}
			if present {
				operations = append(operations, capture.MessageOperation(source))
			}
		}
		appendText(agent.AssembledMessageKindReasoning, turn.thoughtMessageID, "", false, turn.thoughtText, turn.thoughtAdmission)
		appendText(agent.AssembledMessageKindText, turn.assistantMessageID, turn.assistantTextKey, turn.assistantTextKeyResolved, turn.assistantText, turn.assistantAdmission)
		return operations
	})
	if err != nil && !agent.OutputRetained(err) {
		slog.Error("persist buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
	}
}
