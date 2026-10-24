package inputqueue

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// Scheduled input priority. ONE comparator orders every read and every move;
// no query sorts by schedule and no second rule exists in SQL. The store
// captures now once per operation from its clock, because a future item
// becomes immediate when its deadline passes and priority must follow the
// Worker's clock, never a frozen column.

// scheduledFuture reports whether an item's send-not-before instant is still
// ahead of the captured now. A nil or past scheduled_at is immediate.
func scheduledFuture(item StoredItem, now time.Time) bool {
	return item.ScheduledAt != nil && item.ScheduledAt.After(now)
}

// reorderGroup states which reorder group the item belongs to at the captured
// now: "" is the immediate/past-due group, and a future item's group is its
// canonical scheduled_at, so only items with exactly the same scheduled
// instant share a group. The Worker derives it; clients compare it and never
// calculate it, because a browser clock can disagree with the Worker's.
func reorderGroup(item StoredItem, now time.Time) string {
	if !scheduledFuture(item, now) {
		return ""
	}
	return timefmt.Format(*item.ScheduledAt)
}

// compareItemPriority orders queue items for every read and move:
//
//  1. Immediate items (no schedule, or a deadline already passed) first.
//  2. Future groups by ascending scheduled_at.
//  3. Items within one group by order_index, the durable insertion and
//     manual-order tie-breaker.
//
// Instants compare with Before/After, never ==, which a mixed-location or
// monotonic-clock pair would silently misjudge.
func compareItemPriority(a, b StoredItem, now time.Time) int {
	aFuture, bFuture := scheduledFuture(a, now), scheduledFuture(b, now)
	if aFuture != bFuture {
		if aFuture {
			return 1
		}
		return -1
	}
	if aFuture && bFuture {
		if !a.ScheduledAt.Equal(*b.ScheduledAt) {
			if a.ScheduledAt.Before(*b.ScheduledAt) {
				return -1
			}
			return 1
		}
	}
	return cmp.Compare(a.Order, b.Order)
}

// prioritizedItems sorts one read of queue rows into effective order. The
// caller supplies the now its operation captured; the 100-item queue cap keeps
// the sort bounded.
func prioritizedItems(items []StoredItem, now time.Time) []StoredItem {
	slices.SortStableFunc(items, func(a, b StoredItem) int {
		return compareItemPriority(a, b, now)
	})
	return items
}

// nextScheduledAt returns the earliest future deadline among the queued rows.
// It examines every item, not only the head, because immediate work may
// dispatch first and the timer must still fire when the future item matures.
func nextScheduledAt(items []StoredItem, now time.Time) (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, item := range items {
		if !scheduledFuture(item, now) {
			continue
		}
		if !found || item.ScheduledAt.Before(earliest) {
			earliest = *item.ScheduledAt
			found = true
		}
	}
	return earliest, found
}

// autoRetryKind reports whether the kind is one the Worker itself schedules.
// Only the Worker creates these items; external enqueue refuses them, exactly
// as it refuses a question answer.
func autoRetryKind(kind leapmuxv1.AgentInputKind) bool {
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
}

// validateSchedule refuses a schedule that cannot serve its item. An
// automatic-retry item always carries a deadline and never attachments, and
// an ordinary item never carries a retry's provider source payload: a payload
// on the wrong kind reaches no reader and misstates the item's origin.
func validateSchedule(kind leapmuxv1.AgentInputKind, scheduledAt *time.Time, sourcePayload []byte, attachments []Attachment) error {
	if int64(len(sourcePayload)) > int64(MaxItemBytes) {
		return ErrItemTooLarge
	}
	if autoRetryKind(kind) {
		if scheduledAt == nil {
			return fmt.Errorf("%w: an automatic retry requires its scheduled time", ErrInvalidInput)
		}
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
