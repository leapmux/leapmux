package inputqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Automatic retries are queue items, not a side table. The policies here are
// the queue's own: the provider reports WHAT happened (a usage limit with its
// reset instant, or a retryable server failure) and the queue decides WHEN the
// retry dispatches, so pause, recovery, visibility, and ordering treat a retry
// exactly like any other input.

const (
	// Automatic retry messages tell the agent why its work was interrupted.
	autoRetryUsageLimitText  = "You were temporarily interrupted due to usage limit, but the usage limit has been cleared. Please continue working."
	autoRetryServerErrorText = "You were temporarily interrupted due to an infra issue, but the infra issue is now gone. Please continue working."

	// Server-error backoff. Consecutive retries multiply the delay up to the
	// cap; success, cancellation, deletion, or runtime cleanup resets it.
	autoRetryInitialDelay = 10 * time.Second
	autoRetryMaxDelay     = 180 * time.Second
	autoRetryMultiplier   = 2
	autoRetryJitterFrac   = 0.2

	// Usage-limit jitter delays retries after the provider's reset instant
	// so many clients do not retry simultaneously.
	rateLimitJitterMin = 5 * time.Second
	rateLimitJitterMax = 30 * time.Second
	rateLimitJitterPct = 0.05
)

// autoRetryKinds lists the kinds that only the Worker creates, through
// ScheduleAutoRetry. External enqueue refuses them.
var autoRetryKinds = []leapmuxv1.AgentInputKind{
	leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
	leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
}

// IsAutoRetryKind reports whether the kind is one that the Worker itself
// schedules.
func IsAutoRetryKind(kind leapmuxv1.AgentInputKind) bool {
	return slices.Contains(autoRetryKinds, kind)
}

// validateSchedule refuses a schedule or a source payload that cannot serve
// its item. A nil schedule is valid for every kind: nil and a past instant
// are the same immediate item. An automatic retry never carries attachments,
// and an ordinary item never carries a retry's provider source payload: a
// payload on the wrong kind reaches no reader and misstates the item's origin.
func validateSchedule(kind leapmuxv1.AgentInputKind, sourcePayload []byte, attachments []Attachment) error {
	if int64(len(sourcePayload)) > int64(MaxItemBytes) {
		return ErrItemTooLarge
	}
	if IsAutoRetryKind(kind) {
		if len(attachments) != 0 {
			return fmt.Errorf("%w: an automatic retry accepts no attachments", ErrInvalidInput)
		}
		return nil
	}
	if len(sourcePayload) != 0 {
		return fmt.Errorf("%w: only an automatic retry carries a source payload", ErrInvalidInput)
	}
	return nil
}

// usageLimitDeadline derives a usage-limit retry's deadline from the
// provider's reset instant plus a positive jitter.
//
// The result floors to the millisecond, like every deadline: it binds into
// the canonical millisecond-precision DATETIME layout, and the maturity edge
// compares the roundtripped value, so an unfloored instant would never match
// its own write. The jitter is a whole number of milliseconds, so the floor
// can never erase it.
func usageLimitDeadline(resetAt, now time.Time) time.Time {
	return sqltime.FloorMillis(resetAt.Add(positiveRateLimitJitter(resetAt.Sub(now))))
}

// serverErrorDeadline derives a server-error retry's deadline from the
// durable backoff delay, and the delay to store for the next failure.
func serverErrorDeadline(delay time.Duration, now time.Time) (time.Time, time.Duration) {
	delay = max(delay, autoRetryInitialDelay)
	next := min(delay*autoRetryMultiplier, autoRetryMaxDelay)
	return sqltime.FloorMillis(now.Add(delay + symmetricJitter(delay, autoRetryJitterFrac))), next
}

func symmetricJitter(delay time.Duration, frac float64) time.Duration {
	if delay <= 0 || frac <= 0 {
		return 0
	}
	window := float64(delay) * frac
	return time.Duration(window * (2*rand.Float64() - 1))
}

// positiveRateLimitJitter draws a whole number of milliseconds in
// [1 ms, target], where the target is 5% of the remaining window, clamped
// between the minimum and the maximum.
func positiveRateLimitJitter(remaining time.Duration) time.Duration {
	target := time.Duration(math.Round(float64(max(remaining, 0)) * rateLimitJitterPct))
	target = min(max(target, rateLimitJitterMin), rateLimitJitterMax)
	return time.Duration(rand.Int64N(target.Milliseconds())+1) * time.Millisecond
}

// storableSourcePayload returns the provider source that the queue can keep
// for a retry, or nil. The source becomes the accepted row's supplemental
// content, which must be JSON, and the queue caps each item's bytes. A source
// that fails either check is dropped: the retry is the purpose, and the
// source only explains it.
func storableSourcePayload(agentID string, schedule agent.AutoRetrySchedule) []byte {
	payload := schedule.SourcePayload
	if len(payload) == 0 {
		return nil
	}
	if int64(len(payload)) > int64(MaxItemBytes) || !json.Valid(payload) {
		slog.Warn("automatic retry keeps no provider source",
			"agent_id", agentID, "kind", schedule.Kind.String(), "bytes", len(payload),
			"reason", "the source is larger than the item limit or is not JSON")
		return nil
	}
	return payload
}

// ScheduleAutoRetry creates or updates the pending queue item of one retry
// kind and publishes the new snapshot. The item is visible immediately: the
// queue, not a timer table, owns it from the moment the provider asks.
func (m *Manager) ScheduleAutoRetry(ctx context.Context, agentID string, schedule agent.AutoRetrySchedule) error {
	// A retry is queued input like any other, so the agent must accept its
	// kind: a child agent takes messages only, and a row that dispatch can
	// never deliver would fail and pause the queue.
	if err := m.refuseUnacceptedKind(agentID, schedule.Kind); err != nil {
		return err
	}
	schedule.SourcePayload = storableSourcePayload(agentID, schedule)
	snapshot, err := m.mutateAdmitted(agentID, schedule.Admission, func() (Snapshot, bool, error) {
		snapshot, err := m.store.scheduleAutoRetry(ctx, agentID, schedule)
		return snapshot, true, err
	})
	if err != nil {
		return err
	}
	for i := range snapshot.Items {
		item := snapshot.Items[i]
		if item.Kind == schedule.Kind && item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED && item.ScheduledAt != nil {
			slog.Info("automatic retry scheduled", "agent_id", agentID, "kind", schedule.Kind.String(),
				"input_id", item.ID, "scheduled_at", timefmt.Format(*item.ScheduledAt))
			break
		}
	}
	// A schedule whose deadline already passed (a provider reset instant in
	// the past) joins the immediate group and may dispatch at once.
	m.drainIfReleased(agentID, snapshot)
	return nil
}

// CancelAutoRetry removes the pending retry of one kind. A server-error
// cancellation also resets the backoff: the provider cancels exactly when the
// cycle ended without another retryable failure.
func (m *Manager) CancelAutoRetry(ctx context.Context, agentID string, kind leapmuxv1.AgentInputKind, admission agent.AutoRetryAdmission) error {
	snapshot, err := m.mutateAdmitted(agentID, admission, func() (Snapshot, bool, error) {
		return m.store.cancelAutoRetries(ctx, agentID, kind)
	})
	if err == nil {
		m.drainIfReleased(agentID, snapshot)
	}
	return err
}

// CancelAutoRetries removes the pending items of both retry kinds and resets
// the backoff, in one transaction. Runtime cleanup (an agent close, a
// workspace archive, a context clear) calls it, so no schedule outlives the
// lifecycle that created it. It drains nothing because cleanup must not
// dispatch another item while an agent closes or replaces its context.
func (m *Manager) CancelAutoRetries(ctx context.Context, agentID string) error {
	_, err := m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		return m.store.cancelAutoRetries(ctx, agentID, autoRetryKinds...)
	})
	return err
}
