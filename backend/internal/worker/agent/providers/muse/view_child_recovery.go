package muse

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const childViewReadRecoveryTimerTag = "muse-child-view-read-recovery"

func isRecoverableChildViewRead(id string, err error) bool {
	var native *providerkit.JSONRPCResponseError
	if !errors.As(err, &native) || native.Code != -32603 || native.Message != "view range read failed" {
		return false
	}
	var data struct {
		Kind      string `json:"kind"`
		SessionID string `json:"sessionId"`
		Retryable *bool  `json:"retryable"`
	}
	return json.Unmarshal(native.Data, &data) == nil && data.Kind == "internal" && data.SessionID == id &&
		(data.Retryable == nil || *data.Retryable)
}

// A failed native range read supplies no cursor or source proof. Keep the same
// exclusive cursor and recovery owner while consuming the original allowance.
func (a *Agent) readChildViewPage(id string, state *sessionState, recovery *viewRecovery, after *string, deadline time.Time) ([]json.RawMessage, *string, error) {
	delay := 100 * time.Millisecond
	var lastErr error
	for {
		a.stateMu.Lock()
		current := a.currentSessionState(id, state) && state.viewRecovery == recovery
		var failed error
		if current && state.projection != nil {
			failed = state.projection.failed
		}
		a.stateMu.Unlock()
		if !current {
			return nil, nil, errors.Join(lastErr, agent.ErrChildRouteNotReady)
		}
		if failed != nil {
			return nil, nil, errors.Join(lastErr, failed)
		}
		timeout, err := a.childViewTimeout(deadline)
		if err != nil {
			return nil, nil, errors.Join(lastErr, err)
		}
		page, next, err := a.readViewPage(id, after, timeout)
		if err == nil || !isRecoverableChildViewRead(id, err) {
			return page, next, err
		}
		if lastErr == nil {
			slog.Warn("recover the Muse child view read", "session_id", id, "error", err)
		}
		lastErr = err
		if err := a.waitChildViewReadRecovery(id, state, recovery, deadline, delay); err != nil {
			return nil, nil, errors.Join(lastErr, err)
		}
		delay = min(2*delay, time.Second)
	}
}

func (a *Agent) waitChildViewReadRecovery(id string, state *sessionState, recovery *viewRecovery, deadline time.Time, delay time.Duration) error {
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) || state.viewRecovery != recovery {
		a.stateMu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	if state.projection == nil {
		state.projection = newChildProjection()
	}
	projection := state.projection
	a.stateMu.Unlock()
	remaining, err := a.childViewTimeout(deadline)
	if err != nil {
		return err
	}
	timer := a.Clock().NewTimer(min(delay, remaining), childViewReadRecoveryTimerTag)
	defer timer.Stop(childViewReadRecoveryTimerTag)
	for {
		a.stateMu.Lock()
		current := a.currentSessionState(id, state) && state.viewRecovery == recovery && state.projection == projection
		changed, failed := projection.changed, projection.failed
		a.stateMu.Unlock()
		if !current {
			return agent.ErrChildRouteNotReady
		}
		if failed != nil {
			return failed
		}
		select {
		case <-a.Context().Done():
			return a.Context().Err()
		case <-changed:
			// Retirement and proof failure wake the wait without admitting a read.
		case <-timer.C:
			return nil
		}
	}
}
