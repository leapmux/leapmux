package inputqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
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

func validateAutoRetryKey(kind leapmuxv1.AgentInputKind, key string) error {
	if key == "" {
		return nil
	}
	if !IsAutoRetryKind(kind) || strings.TrimSpace(key) == "" || !validateIdentity(key) {
		return fmt.Errorf("%w: automatic retry key is invalid", ErrInvalidInput)
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
	var scheduled scheduledAutoRetry
	snapshot, err := m.mutateAdmitted(agentID, schedule.Admission, func() (Snapshot, bool, error) {
		var snapshot Snapshot
		var err error
		snapshot, scheduled, err = m.store.scheduleAutoRetry(ctx, agentID, schedule)
		if err == nil && scheduled.applied {
			m.enqueueScheduledAutoRetryLocked(agentID, scheduled)
		}
		return snapshot, scheduled.applied, err
	})
	if errors.Is(err, errProviderOwnerReplaced) {
		logIgnoredAutoRetryRequest(agentID, "schedule", schedule.Kind)
		return nil
	}
	if err != nil {
		return err
	}
	if !scheduled.applied {
		return nil
	}
	// A schedule whose deadline already passed (a provider reset instant in
	// the past) joins the immediate group and may dispatch at once.
	m.drainIfReleased(agentID, snapshot)
	return nil
}

// CancelAutoRetry removes the pending retry of one kind. A server-error
// cancellation also resets the backoff: the provider cancels exactly when the
// cycle ended without another retryable failure. The reason is the provider's,
// and the log states it for the retry that the cancellation removes.
func (m *Manager) CancelAutoRetry(ctx context.Context, agentID string, kind leapmuxv1.AgentInputKind, reason string, admission agent.AutoRetryAdmission) error {
	var removed []autoRetryRow
	snapshot, err := m.mutateAdmitted(agentID, admission, func() (Snapshot, bool, error) {
		var snapshot Snapshot
		var err error
		snapshot, removed, err = m.store.cancelAutoRetries(ctx, agentID, kind)
		if err == nil {
			m.enqueueRemovedAutoRetriesLocked(agentID, reason, removed)
		}
		return snapshot, len(removed) > 0, err
	})
	if errors.Is(err, errProviderOwnerReplaced) {
		logIgnoredAutoRetryRequest(agentID, "cancel", kind)
		return nil
	}
	if err != nil {
		return err
	}
	m.drainIfReleased(agentID, snapshot)
	return nil
}

// CancelUsageLimitRetryWithKey removes the queued fallback of one native
// owner. A different owner or explicit user intent remains untouched.
func (m *Manager) CancelUsageLimitRetryWithKey(ctx context.Context, agentID, key, reason string, admission agent.AutoRetryAdmission) error {
	var removed []autoRetryRow
	snapshot, err := m.mutateAdmitted(agentID, admission, func() (Snapshot, bool, error) {
		var snapshot Snapshot
		var err error
		snapshot, removed, err = m.store.cancelUsageRetryWithKey(ctx, agentID, key)
		if err == nil {
			m.enqueueRemovedAutoRetriesLocked(agentID, reason, removed)
		}
		return snapshot, len(removed) > 0, err
	})
	if errors.Is(err, errProviderOwnerReplaced) {
		logIgnoredAutoRetryRequest(agentID, "cancel keyed usage retry", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)
		return nil
	}
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		m.drainIfReleased(agentID, snapshot)
	}
	return nil
}

// CancelAutoRetries removes the pending items of both retry kinds and resets
// the backoff, in one transaction. Runtime cleanup (an agent close, a
// workspace archive, a context clear) calls it, so no schedule outlives the
// lifecycle that created it, and the reason states which cleanup it is. It
// drains nothing because cleanup must not dispatch another item while an
// agent closes or replaces its context.
func (m *Manager) CancelAutoRetries(ctx context.Context, agentID string, reason string) error {
	var removed []autoRetryRow
	_, err := m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		var snapshot Snapshot
		var err error
		snapshot, removed, err = m.store.cancelAutoRetries(ctx, agentID, autoRetryKinds...)
		if err == nil {
			m.enqueueRemovedAutoRetriesLocked(agentID, reason, removed)
		}
		return snapshot, len(removed) > 0, err
	})
	if err != nil {
		return err
	}
	return nil
}

// The log states each event in the life of an automatic retry with its own
// message, so one search finds every event of one kind:
//
//   - "automatic retry scheduled": a schedule created the pending row of a
//     new retry cycle.
//   - "automatic retry rescheduled": a schedule gave the pending row of its
//     kind a new deadline. The line states the deadline that it replaced.
//   - "automatic retry removed": a row left the queue without a delivery. The
//     reason states who removed it and why.
//   - "automatic retry delivered": the provider accepted the retry, and its
//     row became the transcript message with the stated seq.
//
// A deadline of "none" states a row with no schedule, which dispatches as
// soon as the queue reaches it.

// Reasons that the queue itself gives for a removal.
const (
	autoRetryReasonAgentClosed = "the agent closed"
	autoRetryReasonUserDeleted = "the user deleted the retry"
)

// autoRetryRow carries the metadata for retry logs and conditional cancellation.
type autoRetryRow struct {
	id          string
	kind        leapmuxv1.AgentInputKind
	state       leapmuxv1.AgentInputState
	scheduledAt *time.Time
	key         string
}

func autoRetryRowOf(item StoredItem) autoRetryRow {
	return autoRetryRow{id: item.ID, kind: item.Kind, state: item.State, scheduledAt: item.ScheduledAt, key: item.AutoRetryKey}
}

// scheduledAutoRetry states what one schedule did to the pending row of its
// kind. row is the pending row after the schedule. rescheduled reports that
// the schedule moved the existing pending row; otherwise it created the row
// of a new cycle. previousScheduledAt is the deadline that a reschedule
// replaced: nil for a new row, and for a pending row whose edit cleared its
// schedule.
type scheduledAutoRetry struct {
	applied             bool
	row                 autoRetryRow
	rescheduled         bool
	previousScheduledAt *time.Time
}

// errProviderOwnerReplaced reports that the provider owner that asked for a
// retry operation was replaced before the queue admitted the operation, as a
// process restart replaces it. The queue changes nothing for such a request.
var errProviderOwnerReplaced = errors.New("the provider owner that asked was replaced")

// enqueueScheduledAutoRetryLocked retains the log before its snapshot can
// reach an observer. The caller holds this agent's coordinator lock.
// The coordinator inventory never removes or replaces an entry.
func (m *Manager) enqueueScheduledAutoRetryLocked(agentID string, scheduled scheduledAutoRetry) {
	attributes := []any{"agent_id", agentID, "kind", scheduled.row.kind.String(), "input_id", scheduled.row.id,
		"scheduled_at", formatAutoRetryDeadline(scheduled.row.scheduledAt)}
	if !scheduled.rescheduled {
		m.enqueueLogLocked(m.coordinator(agentID), slog.LevelInfo, "automatic retry scheduled", attributes...)
		return
	}
	m.enqueueLogLocked(m.coordinator(agentID), slog.LevelInfo, "automatic retry rescheduled",
		append(attributes, "previous_scheduled_at", formatAutoRetryDeadline(scheduled.previousScheduledAt))...)
}

// enqueueRemovedAutoRetriesLocked retains removals in commit order. The caller
// holds this agent's coordinator lock, as enqueueScheduledAutoRetryLocked does.
func (m *Manager) enqueueRemovedAutoRetriesLocked(agentID, reason string, removed []autoRetryRow) {
	for _, row := range removed {
		m.enqueueLogLocked(m.coordinator(agentID), slog.LevelInfo, "automatic retry removed", "agent_id", agentID, "kind", row.kind.String(), "input_id", row.id,
			"state", row.state.String(), "scheduled_at", formatAutoRetryDeadline(row.scheduledAt), "reason", reason)
	}
}

// logDeliveredAutoRetry records the delivery of an accepted input that is an
// automatic retry, and nothing for any other input.
func logDeliveredAutoRetry(accepted AcceptedTranscript) {
	if !IsAutoRetryKind(accepted.Kind) {
		return
	}
	slog.Info("automatic retry delivered", "agent_id", accepted.AgentID, "kind", accepted.Kind.String(),
		"input_id", accepted.ID, "seq", accepted.Seq)
}

// logIgnoredAutoRetryRequest records a provider request that the queue
// dropped because its provider owner was replaced. The drop is the designed
// outcome, so the line is a debug line.
func logIgnoredAutoRetryRequest(agentID, operation string, kind leapmuxv1.AgentInputKind) {
	slog.Debug("automatic retry request ignored", "agent_id", agentID, "operation", operation,
		"kind", kind.String(), "reason", errProviderOwnerReplaced.Error())
}

func formatAutoRetryDeadline(at *time.Time) string {
	if at == nil {
		return "none"
	}
	return timefmt.Format(*at)
}
