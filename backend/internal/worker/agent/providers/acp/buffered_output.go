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
	queue := agent.OutputQueueFor(b.sink)
	if queue == nil {
		cleanup()
		return
	}
	queue.Append(agent.OutputWrite{Label: "clear completed ACP output state", Run: func() error {
		cleanup()
		return nil
	}})
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Error("complete ACP output before state cleanup", "agent_id", b.AgentID(), "error", err)
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
	if turn.thoughtText != "" && (turn.textBatch == nil || turn.thoughtAdmission != nil) {
		persist(turn.thoughtAdmission, agent.AssembledMessageKindReasoning, turn.thoughtMessageID, turn.thoughtText, completion)
	}
	if turn.assistantText != "" && (turn.textBatch == nil || turn.assistantAdmission != nil) {
		persist(turn.assistantAdmission, agent.AssembledMessageKindText, turn.assistantMessageID, turn.assistantText, completion)
	}
	if turn.textBatch == nil {
		return
	}
	// Root transcript admissions already reserved their own positions. This
	// batch holds the remaining text until preparation can run off the reader.
	err := turn.textBatch.Prepare(func() []agent.CapturedTranscriptOperation {
		const source = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		var operations []agent.CapturedTranscriptOperation
		appendText := func(kind agent.AssembledMessageKind, messageID, text string, admission *acpTranscriptAdmission) {
			if text == "" || admission != nil {
				return
			}
			template, prepare, err := c.preparedAssembledText(kind, messageID, text, completion)
			if err != nil {
				slog.Error("prepare buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
				return
			}
			template.AgentSessionID = turn.sessionID
			capture, present, err := agent.CapturePreparedTranscript(c.sink(), source, template, agent.SpanInfo{}, prepare)
			if err != nil {
				slog.Error("capture buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
				return
			}
			if present {
				operations = append(operations, capture.MessageOperation(source))
			}
		}
		appendText(agent.AssembledMessageKindReasoning, turn.thoughtMessageID, turn.thoughtText, turn.thoughtAdmission)
		appendText(agent.AssembledMessageKindText, turn.assistantMessageID, turn.assistantText, turn.assistantAdmission)
		return operations
	})
	if err != nil && !agent.OutputRetained(err) {
		slog.Error("persist buffered ACP text", "agent_id", c.b.AgentID(), "error", err)
	}
}
