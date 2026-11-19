package goose

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// gooseCompactCommand is the prompt that compacts the conversation. Goose runs
// it as a turn of its own, which the queue expects of a compaction.
const gooseCompactCommand = "/compact"

// Agent compacts through Goose's own /compact turn.
var _ agent.ContextCompactor = (*Agent)(nil)

// CompactContext runs Goose's `/compact` turn. Goose states no compaction
// frame of its own, so the worker publishes the live compaction while the turn
// runs, and the turn's end stores the boundary row the transcript keeps
// (decision 21: live start, one stored end).
func (a *Agent) CompactContext() error {
	err := a.SendPreparedPrompt(gooseCompactCommand, nil, func(string) error {
		a.Mu.Lock()
		a.promptOutput.compactPending = true
		a.Mu.Unlock()
		providerkit.PublishCompaction(a.Sink(), "manual")
		return nil
	})
	if err == nil {
		return nil
	}
	// The send failed, so no turn will end the compaction this request began.
	a.Mu.Lock()
	pending := a.promptOutput.compactPending
	a.promptOutput.compactPending = false
	a.Mu.Unlock()
	if pending && !a.PromptActive() {
		providerkit.EndCompaction(a.Sink())
	}
	return err
}
