package kilo

import (
	"encoding/json"
	"sync"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

// SteerInput preserves Kilo's second ACP prompt as a turn of its own and
// returns its native receipt when Kilo's daemon route admits the prompt. Kilo
// can answer that prompt after the first response. Without this turn, the first
// response clears the text buffer and no callback persists the later answer.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	release, err := a.TrackRootPromptCallback()
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	sessionID, err := a.beginKiloSteer()
	if err != nil {
		release()
		return agent.NativeInputReceipt{}, err
	}
	inputID := id.Generate()
	receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: inputID, TurnID: inputID}
	finish := func(response json.RawMessage, err error) {
		defer release()
		a.finishKiloSteer(sessionID, response, err, true)
	}
	var nativeSend func(func(json.RawMessage, error)) error
	err = a.SendPromptDetachedUsing(sessionID, request.Content, request.Attachments, func(prepared acp.PreparedPrompt) error {
		send, err := a.prepareNativePrompt(receipt, prepared)
		if err != nil {
			return err
		}
		if send == nil {
			receipt = agent.NativeInputReceipt{}
			params, err := prepared.MarshalParams()
			if err != nil {
				return err
			}
			return a.SendDetachedRequest(acp.MethodSessionPrompt, params, providerkit.DetachedRequestOptions{}, finish)
		}
		nativeSend = send
		return nil
	})
	if err == nil && nativeSend != nil {
		// Run native HTTP submission after SendPromptDetachedUsing releases
		// sessionMu so Interrupt, ClearContext, and prompt completions do not stall.
		err = nativeSend(finish)
	}
	if err != nil {
		release()
		a.finishKiloSteer(sessionID, nil, err, false)
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}

func (a *Agent) beginKiloSteer() (string, error) {
	a.steers.mu.Lock()
	defer a.steers.mu.Unlock()
	_, active, sessionID, _ := a.SteerTarget()
	if !active {
		return "", agent.ErrNoActiveTurn
	}
	if a.steers.pending == 0 || a.steers.sessionID != sessionID {
		a.steers.sessionID = sessionID
		a.steers.pending = 0
		a.steers.ownsTurn = a.BeginAgentTurn()
		a.steers.response = nil
	}
	a.steers.pending++
	return sessionID, nil
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
