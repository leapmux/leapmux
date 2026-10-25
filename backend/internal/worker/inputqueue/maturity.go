package inputqueue

import (
	"context"
	"time"

	"github.com/coder/quartz"
)

// Maturity: a scheduled item crosses from future to immediate when the
// Worker's wall clock reaches its scheduled_at. Nothing in the database
// changes at that instant, so the queue owes each such crossing one edge: a
// revision bump that publishes the matured order, then an ordinary drain.
// PrepareDispatch stays the sole delivery authority; the edge only asks.
//
// One timer per agent serves the earliest future deadline. Three rules make
// every deadline deliver its edge:
//
//   - The snapshot's own instant decides what is future: the timer arms for
//     Snapshot.NextDeadline. An item that a published snapshot shows as
//     scheduled therefore always has a timer, even when the deadline passes
//     between the snapshot and the arm.
//   - A deadline that the wall clock already reached is due, and nothing
//     cancels it. A timer that has not fired yet fires at once instead; a
//     fired one keeps its slot until its callback runs, because the callback
//     arms the next deadline itself.
//   - The timer waits at most maturityRecheckInterval. A timer measures the
//     monotonic clock, which stops during a host sleep, while the deadline is
//     a wall-clock instant; the recheck limits how late a deadline can
//     deliver after a sleep or a forward clock step.

// maturityRecheckInterval is the longest that the maturity timer sleeps
// before it reads the wall clock again.
const maturityRecheckInterval = time.Minute

// queueMaturityTimerTag identifies the maturity timer on the clock, so a test
// can trap its arm and its stop by one tag.
const queueMaturityTimerTag = "queue-maturity"

// maturityTimer is a coordinator's one timer for the earliest scheduled
// deadline. The coordinator's lock guards it.
type maturityTimer struct {
	timer *quartz.Timer
	// at is the deadline that the timer serves.
	at time.Time
	// arm identifies the current arm, so a callback clears only its own slot.
	arm uint64
	// next is the earliest future deadline of the newest snapshot. A callback
	// that finds its deadline not yet due arms from it, because a read that
	// saw the fired timer could not arm a newer, earlier deadline itself.
	next    time.Time
	hasNext bool
}

// rearmMaturityLocked keeps the coordinator's maturity timer on the earliest
// deadline that the snapshot shows as future, under the rules above. Every
// published snapshot reaches it, and so does every plain read, because time
// moved since the last one. The caller holds c.mu.
func (m *Manager) rearmMaturityLocked(c *coordinator, snapshot Snapshot) {
	if c.retired || m.isStopped() {
		return
	}
	now := m.store.now()
	slot := &c.maturity
	slot.next, slot.hasNext = snapshot.NextDeadline, !snapshot.NextDeadline.IsZero()
	if slot.timer != nil {
		if !now.Before(slot.at) {
			// Due. A timer that has not fired yet fires now; a fired one is
			// waiting for c.mu, and its callback arms what comes next.
			if slot.timer.Stop(queueMaturityTimerTag) {
				m.armMaturityLocked(c, snapshot.AgentID, slot.at)
			}
			return
		}
		if slot.hasNext && slot.next.Equal(slot.at) {
			return
		}
		if !slot.timer.Stop(queueMaturityTimerTag) {
			// It fired while the wall clock still read before its deadline.
			// The callback arms from slot.next when it runs.
			return
		}
		slot.timer = nil
	}
	if slot.hasNext {
		m.armMaturityLocked(c, snapshot.AgentID, slot.next)
	}
}

// armMaturityLocked arms the coordinator's timer for one deadline. A passed
// deadline arms with no delay, and a far one waits for the recheck first.
// The caller holds c.mu, and the slot holds no live timer.
func (m *Manager) armMaturityLocked(c *coordinator, agentID string, at time.Time) {
	m.armMaturityAfterLocked(c, agentID, at, at.Sub(m.store.now()))
}

// armMaturityAfterLocked arms the timer for a deadline after the given delay,
// capped at the recheck interval. The caller holds c.mu, and the slot holds
// no live timer.
func (m *Manager) armMaturityAfterLocked(c *coordinator, agentID string, at time.Time, delay time.Duration) {
	delay = min(max(delay, 0), maturityRecheckInterval)
	c.maturity.arm++
	arm := c.maturity.arm
	c.maturity.at = at
	c.maturity.timer = m.store.clock.AfterFunc(delay, func() {
		m.maturityReached(agentID, arm, at)
	}, queueMaturityTimerTag)
}

// stopMaturityLocked stops the coordinator's timer. A callback already in
// flight still runs, and it finds its slot cleared. The caller holds c.mu.
func stopMaturityLocked(c *coordinator) {
	if c.maturity.timer != nil {
		c.maturity.timer.Stop(queueMaturityTimerTag)
		c.maturity.timer = nil
	}
}

// maturityReached is the callback of one arm. When the wall clock reached
// the deadline, it records the edge and asks for a drain; otherwise -- a
// recheck, or a timer that ran ahead of the wall clock -- it arms again.
func (m *Manager) maturityReached(agentID string, arm uint64, at time.Time) {
	if !m.beginActivity() {
		return
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.maturity.arm == arm {
		c.maturity.timer = nil
	}
	if c.retired {
		c.mu.Unlock()
		return
	}
	if m.store.now().Before(at) {
		if c.maturity.timer == nil && c.maturity.hasNext {
			m.armMaturityLocked(c, agentID, c.maturity.next)
		}
		c.mu.Unlock()
		return
	}
	snapshot, changed, err := m.store.MatureScheduled(context.Background(), agentID, at)
	if err != nil {
		m.enqueueFailureLocked(c, "agent input queue maturity failed", "agent_id", agentID, "error", err)
		// The edge is still owed. The recheck tries again, so a transient
		// database error delays the delivery instead of losing it.
		if c.maturity.timer == nil {
			m.armMaturityAfterLocked(c, agentID, at, maturityRecheckInterval)
		}
		m.unlockAndPublish(c)
		return
	}
	if changed {
		m.enqueueSnapshotLocked(c, snapshot)
	} else {
		m.rearmMaturityLocked(c, snapshot)
	}
	m.unlockAndPublish(c)
	m.drainIfReleased(agentID, snapshot)
}
