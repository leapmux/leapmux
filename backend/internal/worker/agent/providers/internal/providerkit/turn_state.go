package providerkit

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// PublishSteerableTurnActiveTo reports a turn that accepts steering. Codex
// uses this for turn/started because its app-server accepts turn/steer for each
// active turn, including a turn that a goal continuation starts. Other
// providers call PublishTurnStateTo with the steerability that their protocol
// states.
func PublishSteerableTurnActiveTo(sink agent.TurnServices, active bool, seq uint64) agent.TurnState {
	return PublishTurnStateTo(sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

// PublishTurnStateTo reports state to sink with the ordering token seq, and
// returns state.
//
// A NIL sink publishes nowhere. Tests construct bare agents by long-standing
// convention -- `&claude.Agent{turnActive: true}` and its like appear dozens
// of times -- and a turn flag is read on paths those tests drive. The
// JSONRPCProcess hook takes the same stance for the same reason: nobody is
// listening, so there is nothing to say. Every agent the Worker builds has a
// sink, so this is never nil in production.
func PublishTurnStateTo(sink agent.TurnServices, state agent.TurnState, seq uint64) agent.TurnState {
	if sink != nil {
		sink.SetTurnState(state, seq)
	}
	return state
}

// TurnSeq issues the ordering token that goes with a provider's turn
// flag. Read the token in the SAME critical section that reads the flag: the
// pair is what lets the Worker tell a publish it overtook from a current one,
// and a token taken outside that section orders nothing.
//
// It is a plain counter under the provider's own lock rather than an atomic,
// because the lock is what makes the pair atomic. An atomic would let the two
// reads separate again, which is the defect this exists to close.
type TurnSeq struct {
	seq uint64
}

// NextTurnSeq issues the next token. The caller must hold the lock that guards
// the provider's turn flag.
func (t *TurnSeq) NextTurnSeq() uint64 {
	t.seq++
	return t.seq
}
