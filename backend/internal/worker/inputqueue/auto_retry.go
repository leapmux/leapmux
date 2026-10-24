package inputqueue

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Automatic retries are queue items, not a side table. The policies here are
// the queue's own: the provider reports WHAT happened (a usage limit with its
// reset instant, or a retryable server failure) and the queue decides WHEN the
// retry dispatches, so pause, recovery, visibility, and ordering treat a retry
// exactly like any other input.

const (
	// autoRetryText is the synthetic turn every automatic retry sends.
	autoRetryText = "Continue."

	// Server-error backoff. Consecutive retries double the delay up to the
	// cap; success, cancellation, deletion, or runtime cleanup resets it.
	autoRetryInitialDelay = 10 * time.Second
	autoRetryMaxDelay     = 180 * time.Second
	autoRetryJitterFrac   = 0.2

	// Usage-limit jitter, added to the provider's own reset instant so a
	// horde of clients does not stampede the moment the window lifts.
	rateLimitJitterMin = 5 * time.Second
	rateLimitJitterMax = 30 * time.Second
	rateLimitJitterPct = 0.05
)

// autoRetryDeadline derives one retry's deadline and the delay to store for
// the next failure. readDelay supplies the durable server-error backoff from
// the caller's transaction, so the deadline and its successor commit atomically.
//
// All results floor to the millisecond: the deadline binds into the canonical
// millisecond-precision DATETIME layout, and the maturity guard compares the
// roundtripped value, so an unfloored instant would never match its own write.
func autoRetryDeadline(kind leapmuxv1.AgentInputKind, providerAt time.Time, now time.Time, readDelay func() (time.Duration, error)) (time.Time, time.Duration, error) {
	if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT {
		return sqltime.FloorMillis(providerAt.Add(positiveRateLimitJitter(providerAt.Sub(now)))), 0, nil
	}
	delay, err := readDelay()
	if err != nil {
		return time.Time{}, 0, err
	}
	if delay < autoRetryInitialDelay {
		delay = autoRetryInitialDelay
	}
	jitter := symmetricJitter(delay, autoRetryJitterFrac)
	next := time.Duration(float64(delay) * 2)
	if next > autoRetryMaxDelay {
		next = autoRetryMaxDelay
	}
	return sqltime.FloorMillis(now.Add(delay + jitter)), next, nil
}

func symmetricJitter(delay time.Duration, frac float64) time.Duration {
	if delay <= 0 || frac <= 0 {
		return 0
	}
	window := float64(delay) * frac
	return time.Duration(window * (2*rand.Float64() - 1))
}

func positiveRateLimitJitter(remaining time.Duration) time.Duration {
	if remaining < 0 {
		remaining = 0
	}
	target := time.Duration(math.Round(float64(remaining) * rateLimitJitterPct))
	if target < rateLimitJitterMin {
		target = rateLimitJitterMin
	}
	if target > rateLimitJitterMax {
		target = rateLimitJitterMax
	}
	return time.Duration(rand.Int64N(int64(target))) + 1
}

// ScheduleAutoRetry creates or updates the pending queue item of one retry
// kind and publishes the new snapshot. The item is visible immediately: the
// queue, not a timer table, owns it from the moment the provider asks.
func (m *Manager) ScheduleAutoRetry(ctx context.Context, agentID string, schedule agent.AutoRetrySchedule) error {
	snapshot, err := m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		snapshot, err := m.store.scheduleAutoRetry(ctx, agentID, schedule)
		return snapshot, true, err
	})
	if err != nil {
		return err
	}
	// A schedule whose deadline already passed (a provider reset instant in
	// the past) joins the immediate group and may dispatch at once.
	m.drainIfReleased(agentID, snapshot)
	return nil
}

// CancelAutoRetry removes the pending retry of one kind. A server-error
// cancellation also resets the backoff: the provider cancels exactly when the
// cycle ended without another retryable failure.
func (m *Manager) CancelAutoRetry(ctx context.Context, agentID string, kind leapmuxv1.AgentInputKind) error {
	_, err := m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.cancelAutoRetry(ctx, agentID, kind, true)
	})
	return err
}

// CancelAutoRetries removes both retry kinds' pending items and resets the
// backoff. Runtime cleanup (an agent close, a workspace archive) calls it, so
// no schedule outlives the lifecycle that created it.
func (m *Manager) CancelAutoRetries(ctx context.Context, agentID string) error {
	for _, kind := range []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	} {
		if _, err := m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
			return m.store.cancelAutoRetry(ctx, agentID, kind, true)
		}); err != nil {
			return err
		}
	}
	return nil
}
