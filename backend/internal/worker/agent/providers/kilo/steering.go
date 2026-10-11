package kilo

import (
	"encoding/json"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// kiloSteerState holds the detached prompts that share one active turn.
// Kilo can answer them after the prompt that received the user's first input.
type kiloSteerState struct {
	mu        sync.Mutex
	sessionID string
	pending   int
	ownsTurn  bool
	response  json.RawMessage
}

// SteerInput preserves Kilo's second ACP prompt as a turn of its own. Kilo can
// answer that prompt after the first response. Without this turn, the first
// response clears the text buffer and no callback persists the later answer.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.steers.mu.Lock()
	if !a.PromptActive() {
		a.steers.mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	sessionID := a.CurrentSessionID()
	if a.steers.pending == 0 || a.steers.sessionID != sessionID {
		a.steers.sessionID = sessionID
		a.steers.pending = 0
		a.steers.ownsTurn = a.BeginAgentTurn()
		a.steers.response = nil
	}
	a.steers.pending++
	a.steers.mu.Unlock()

	err := a.SendPromptDetached(content, attachments, func(response json.RawMessage, err error) {
		a.finishKiloSteer(sessionID, response, err, true)
	})
	if err != nil {
		a.finishKiloSteer(sessionID, nil, err, false)
	}
	return err
}

func (a *Agent) finishKiloSteer(sessionID string, response json.RawMessage, err error, reportFailure bool) {
	a.steers.mu.Lock()
	if a.steers.pending == 0 || a.steers.sessionID != sessionID {
		a.steers.mu.Unlock()
		return
	}
	if err == nil && len(response) > 0 {
		a.steers.response = append(a.steers.response[:0], response...)
	}
	a.steers.pending--
	last := a.steers.pending == 0
	ownsTurn := a.steers.ownsTurn
	end := append(json.RawMessage(nil), a.steers.response...)
	if last {
		a.steers.ownsTurn = false
		a.steers.response = nil
	}
	a.steers.mu.Unlock()

	if reportFailure {
		a.ReportSteerFailure(err)
	}
	if !last || !ownsTurn || a.CurrentSessionID() != sessionID {
		return
	}
	if len(end) == 0 {
		a.EndAgentTurnWithoutRow()
		return
	}
	a.EndAgentTurn(end)
}
