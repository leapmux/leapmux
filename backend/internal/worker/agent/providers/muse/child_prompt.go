package muse

import "github.com/leapmux/leapmux/internal/worker/agent"

// childOpeningPrompt retains the first immutable assignment until its writer
// accepts it. Later native metadata cannot replace that original input.
type childOpeningPrompt struct {
	writer       agent.ProviderServices
	childAgentID string
	text         string
}

// retainChildOpeningPrompt runs on the serialized child view actor after a
// hard refusal. Neither native reads nor child output can precede acceptance.
func (a *Agent) retainChildOpeningPrompt(id string, state *sessionState) error {
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) {
		a.stateMu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	pending := state.openingPrompt
	a.stateMu.Unlock()
	if pending == nil {
		return nil
	}
	err := pending.writer.PersistChildPrompt(pending.childAgentID, pending.text)
	if !agent.OutputRetained(err) {
		return err
	}
	a.stateMu.Lock()
	if state.openingPrompt == pending {
		state.openingPrompt = nil
	}
	a.stateMu.Unlock()
	return nil
}
