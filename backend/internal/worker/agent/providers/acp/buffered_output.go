package acp

import (
	"errors"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// enqueueTurnOutputCleanup keeps provider cleanup after every submitted output row.
// A process exit drains all turns. An ordinary boundary preserves a queued agent turn.
func (b *Base) enqueueTurnOutputCleanup(allTurns bool) {
	b.Mu.Lock()
	capture := b.hooks.CaptureTurnOutputCleanup
	if capture == nil || (!allTurns && b.agentTurnQueued) {
		b.Mu.Unlock()
		return
	}
	cleanup := capture()
	b.Mu.Unlock()
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
	if turn.thoughtText != "" {
		c.persistFinishedText(agent.AssembledMessageKindReasoning, turn.thoughtMessageID, turn.thoughtText, completion)
	}
	if turn.assistantText != "" {
		c.persistFinishedText(agent.AssembledMessageKindText, turn.assistantMessageID, turn.assistantText, completion)
	}
}
