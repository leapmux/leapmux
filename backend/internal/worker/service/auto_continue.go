package service

import (
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	autoContinueInitialDelay = 10 * time.Second
	autoContinueMaxDelay     = 180 * time.Second
	autoContinueMultiplier   = 2.0
	autoContinueJitterFrac   = 0.2
)

// autoContinueState tracks exponential-backoff retry state for a single agent.
// Note: hub/backoff.go serves WebSocket reconnection (a blocking retry loop),
// whereas this is a timer-based scheduler with generation tracking — different
// enough that sharing code would add complexity without benefit.
type autoContinueState struct {
	mu         sync.Mutex
	timer      *time.Timer
	generation int
	backoff    time.Duration
}

// scheduleAutoContinue schedules a "Continue." message for the given agent
// after an exponentially increasing delay. Each call resets the timer.
func (h *OutputHandler) scheduleAutoContinue(agentID string) {
	v, ok := h.autoContinue.Load(agentID)
	if !ok {
		v, _ = h.autoContinue.LoadOrStore(agentID, &autoContinueState{
			backoff: autoContinueInitialDelay,
		})
	}
	st := v.(*autoContinueState)

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.timer != nil {
		st.timer.Stop()
	}

	st.generation++
	gen := st.generation

	delay := st.backoff
	if delay > autoContinueMaxDelay {
		delay = autoContinueMaxDelay
	}

	// Apply jitter: +-20%.
	jitter := time.Duration(float64(delay) * autoContinueJitterFrac * (2*rand.Float64() - 1))
	delay += jitter

	// Advance backoff for next invocation.
	st.backoff = time.Duration(float64(st.backoff) * autoContinueMultiplier)

	slog.Info("auto-continue scheduled",
		"agent_id", agentID,
		"delay", delay,
		"generation", gen)

	st.timer = time.AfterFunc(delay, func() {
		st.mu.Lock()
		if st.generation != gen {
			st.mu.Unlock()
			return
		}
		st.mu.Unlock()

		if h.sendMessageFunc == nil {
			slog.Warn("auto-continue: sendMessageFunc not set", "agent_id", agentID)
			return
		}

		slog.Info("auto-continue firing", "agent_id", agentID)
		h.sendMessageFunc(agentID, "Continue.")
	})
}

// resetAutoContinue cancels any pending auto-continue timer and resets
// the backoff to the initial delay.
func (h *OutputHandler) resetAutoContinue(agentID string) {
	v, ok := h.autoContinue.Load(agentID)
	if !ok {
		return
	}
	st := v.(*autoContinueState)

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.generation++
	st.backoff = autoContinueInitialDelay
}

// cleanupAutoContinue stops any pending timer and removes the state.
func (h *OutputHandler) cleanupAutoContinue(agentID string) {
	v, ok := h.autoContinue.LoadAndDelete(agentID)
	if !ok {
		return
	}
	st := v.(*autoContinueState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}
