package pi

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const piRetryCancellationError = "Retry cancelled"

type piRetryTurn struct {
	generation uint64
	attempt    int
	backoff    bool
}

// Mu protects retry ownership. Cancelled runs retain their own settlement epoch.
// A replacement start cannot give an old agent_settled ownership of the new turn.
type piRetryState struct {
	turn        *piRetryTurn
	settlements []uint64
}

func (state *piRetryState) cancelled(generation uint64) bool {
	return slices.Contains(state.settlements, generation)
}

func (a *Agent) handlePiRetryEvent(kind string, raw []byte) {
	if kind == contracts.PiEventAutoRetryStart {
		var event struct {
			Attempt     int    `json:"attempt"`
			MaxAttempts int    `json:"maxAttempts"`
			DelayMs     *int64 `json:"delayMs"`
		}
		if json.Unmarshal(raw, &event) != nil || event.Attempt <= 0 || event.MaxAttempts < event.Attempt || event.DelayMs == nil || *event.DelayMs < 0 {
			return
		}
		a.Mu.Lock()
		defer a.Mu.Unlock()
		turn := a.retryState.turn
		if turn != nil && turn.generation == a.turnGeneration && a.currentTurnActive && turn.attempt == 0 {
			turn.attempt = event.Attempt
			turn.backoff = true
		}
		return
	}
	var event struct {
		Success    *bool  `json:"success"`
		Attempt    int    `json:"attempt"`
		FinalError string `json:"finalError"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Success == nil || event.Attempt <= 0 {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	turn := a.retryState.turn
	if turn == nil || turn.attempt != event.Attempt {
		return
	}
	if !*event.Success && event.FinalError == piRetryCancellationError && turn.backoff {
		a.retryState.settlements = append(a.retryState.settlements, turn.generation)
	}
	a.retryState.turn = nil
}

// handlePiAgentSettled ends a cancelled retry whose earlier agent_end kept the turn open.
// The native notification and divider remain unchanged. The epoch identifies their owner.
func (a *Agent) handlePiAgentSettled(raw []byte) {
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != contracts.PiEventAgentSettled {
		return
	}
	a.Mu.Lock()
	if len(a.retryState.settlements) == 0 {
		a.Mu.Unlock()
		return
	}
	generation := a.retryState.settlements[0]
	a.retryState.settlements = a.retryState.settlements[1:]
	current := generation == a.turnGeneration && a.currentTurnActive
	if current {
		a.currentTurnActive = false
		a.turnStartedAt = time.Time{}
		a.TurnToolUses = 0
		a.interruptRequests = nil
		a.retryState.turn = nil
	}
	a.Mu.Unlock()
	if current {
		a.sink.ReportProgress(agent.ResetProgress())
		a.PublishTurnActive()
	}
}
