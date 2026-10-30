package acp

import (
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// PublishTurnActive implements Agent for every ACP provider. Active state
// reaches the sink immediately. Inactive state follows pending output and
// retry operations, including when the manager republishes after a busy refusal.
func (b *Base) PublishTurnActive() agent.TurnState {
	b.Mu.Lock()
	state, sequence := b.turnStateLocked(), b.NextTurnSeq()
	publish := b.publishTurnActive
	queue := agent.OutputQueueFor(b.sink)
	queued := publish != nil && !state.Active && queue != nil
	if queued {
		// Reserve before Mu releases. A manual completion reserves its verdict
		// under the same lock, so an idle report cannot enter ahead of that verdict.
		queue.Append(agent.OutputWrite{Label: "publish inactive ACP turn", Run: func() error { publish(state, sequence); return nil }})
	}
	b.Mu.Unlock()
	if publish == nil {
		return state
	}
	if queued {
		if err := agent.FlushPendingOutput(b.sink); err != nil {
			slog.Error("retain ACP turn publication", "agent_id", b.AgentID(), "error", err)
		}
	} else {
		publish(state, sequence)
	}
	return state
}

// turnStateLocked derives the queue state from its independent owners.
// The caller holds Mu.
func (b *Base) turnStateLocked() agent.TurnState {
	return agent.TurnState{Active: b.promptActive || b.clientWork != nil, Steerable: b.promptActive && b.steersLocked()}
}
