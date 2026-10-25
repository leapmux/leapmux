package inputqueue

import (
	"cmp"
	"slices"
	"time"

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

// reorderSegments states the reorder segment of each prioritized item at the
// captured now. A segment is a maximal run of contiguous, unlocked items that
// share one reorder group, and a move is legal only inside one segment. A
// locked item (a dispatch in flight, a steer that awaits its echo) belongs to
// no segment, shown as 0, and ends the segment before it. Segments count from
// 1, and the numbers mean nothing outside one snapshot.
//
// It is the one definition of the rule: Store.Move refuses a move across
// segments, and each snapshot sends every item's segment, so a client offers
// exactly the moves that Move accepts.
func reorderSegments(items []StoredItem, now time.Time) []uint32 {
	segments := make([]uint32, len(items))
	var segment uint32
	previous := -1 // the previous item of the current run, or -1 after a lock
	for i := range items {
		if lockedInputState(items[i].State) {
			previous = -1
			continue
		}
		if previous < 0 || reorderGroup(items[previous], now) != reorderGroup(items[i], now) {
			segment++
		}
		segments[i] = segment
		previous = i
	}
	return segments
}

// compareItemPriority orders queue items for every read and move:
//
//  1. Immediate items (no schedule, or a deadline already passed) first.
//  2. Future groups by ascending scheduled_at.
//  3. Items within one group by order_index, the durable insertion and
//     manual-order tie-breaker. A dispatch also writes it (see
//     placeDispatchedItem), so an item that matures later never sorts ahead
//     of input that the queue already attempted.
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

// nextScheduledAt returns the earliest deadline among the items that are
// still future at the given instant, or zero when none is. It examines every
// item, not only the head, because immediate work may dispatch first and the
// maturity timer must still fire when the future item matures.
func nextScheduledAt(items []StoredItem, now time.Time) time.Time {
	var earliest time.Time
	for i := range items {
		if scheduledFuture(items[i], now) && (earliest.IsZero() || items[i].ScheduledAt.Before(earliest)) {
			earliest = *items[i].ScheduledAt
		}
	}
	return earliest
}

// prioritizedItems sorts one read of queue rows into effective order. The
// caller supplies the now its operation captured; the 100-item queue cap keeps
// the sort small.
func prioritizedItems(items []StoredItem, now time.Time) []StoredItem {
	slices.SortStableFunc(items, func(a, b StoredItem) int {
		return compareItemPriority(a, b, now)
	})
	return items
}
