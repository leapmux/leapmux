package crdt_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func TestPresence_HeartbeatChangesActive(t *testing.T) {
	now := time.Unix(0, 0)
	p := crdt.NewPresenceTracker(func() time.Time { return now })

	active, changed := p.Heartbeat("ws1", "alice")
	assert.Equal(t, "alice", active)
	assert.True(t, changed)

	// A second heartbeat from the same client doesn't flip activeness.
	now = now.Add(time.Second)
	active, changed = p.Heartbeat("ws1", "alice")
	assert.Equal(t, "alice", active)
	assert.False(t, changed)

	// Bob heartbeats *strictly later* than alice — bob takes over.
	now = now.Add(time.Second)
	active, changed = p.Heartbeat("ws1", "bob")
	assert.Equal(t, "bob", active)
	assert.True(t, changed)

	// Two clients heartbeating at the same instant tie; activeness
	// is empty (no clear leader).
	now = now.Add(time.Second)
	_, _ = p.Heartbeat("ws1", "carol")
	active, _ = p.Heartbeat("ws1", "dan") // same `now`
	assert.Equal(t, "", active)
}

func TestPresence_IdleEntriesStayActive(t *testing.T) {
	now := time.Unix(0, 0)
	p := crdt.NewPresenceTracker(func() time.Time { return now })
	p.Heartbeat("ws1", "alice")

	// No inactivity window — even after an hour without a heartbeat,
	// alice is still the active client. The manager evicts via
	// RemoveClient on WS disconnect, not by elapsed time.
	now = now.Add(time.Hour)
	assert.Equal(t, "alice", p.Active("ws1"))
}

func TestPresence_RemoveClientDropsEntriesAndReportsChange(t *testing.T) {
	now := time.Unix(0, 0)
	p := crdt.NewPresenceTracker(func() time.Time { return now })

	// alice claims ws1 + ws2; bob also heartbeats ws1 later (so bob is
	// the active client there, alice on ws2).
	p.Heartbeat("ws1", "alice")
	p.Heartbeat("ws2", "alice")
	now = now.Add(time.Second)
	p.Heartbeat("ws1", "bob")

	// Drop alice. ws1's active stays bob (no change). ws2's active
	// flips alice → "" (the workspace had only her).
	changes := p.RemoveClient("alice")
	assert.Equal(t, map[string]string{"ws2": ""}, changes)
	assert.Equal(t, "bob", p.Active("ws1"))
	assert.Equal(t, "", p.Active("ws2"))

	// Dropping bob now empties ws1 — change reported.
	changes = p.RemoveClient("bob")
	assert.Equal(t, map[string]string{"ws1": ""}, changes)
}

func TestPresence_RemoveClientUnknownIsNoop(t *testing.T) {
	p := crdt.NewPresenceTracker(nil)
	p.Heartbeat("ws1", "alice")
	changes := p.RemoveClient("ghost")
	assert.Empty(t, changes)
	assert.Equal(t, "alice", p.Active("ws1"))
}

// TestPresence_SweepInactive_DropsOldRowsAndReportsChange pins down
// the defense-in-depth reaper: entries whose last heartbeat predates
// the cutoff are dropped, and a workspace whose active client gets
// reaped must report the resulting leader transition so the manager
// can broadcast it. A stale heartbeat that's still recent enough
// stays.
func TestPresence_SweepInactive_DropsOldRowsAndReportsChange(t *testing.T) {
	now := time.Unix(0, 0)
	p := crdt.NewPresenceTracker(func() time.Time { return now })

	// Two workspaces, two clients each. alice on ws1 + ws2 at t=0.
	// bob on ws1 at t=1 (so bob is currently the active there).
	p.Heartbeat("ws1", "alice")
	p.Heartbeat("ws2", "alice")
	now = now.Add(time.Second)
	p.Heartbeat("ws1", "bob")

	// Advance time well past the cutoff for alice but keep bob fresh:
	// cutoff = now - 500ms catches alice's t=0 entry on ws2, but
	// crucially we re-heartbeat bob on ws1 right before the sweep so
	// bob survives.
	now = now.Add(2 * time.Second)
	p.Heartbeat("ws1", "bob")
	cutoff := now.Add(-500 * time.Millisecond)
	changes := p.SweepInactive(cutoff)

	// ws2 had only alice → leader goes alice → "". ws1 still has bob
	// as live leader (bob was previously active, still active → no
	// change reported even though alice's entry was reaped).
	assert.Equal(t, map[string]string{"ws2": ""}, changes)
	assert.Equal(t, "bob", p.Active("ws1"))
	assert.Equal(t, "", p.Active("ws2"))
}

// TestPresence_SweepInactive_KeepsFreshEntries asserts the sweep is
// idempotent when every entry is newer than the cutoff: no changes
// reported and the active-client map is untouched.
func TestPresence_SweepInactive_KeepsFreshEntries(t *testing.T) {
	now := time.Unix(0, 0)
	p := crdt.NewPresenceTracker(func() time.Time { return now })
	p.Heartbeat("ws1", "alice")
	p.Heartbeat("ws2", "bob")

	cutoff := now.Add(-time.Hour)
	changes := p.SweepInactive(cutoff)
	assert.Empty(t, changes)
	assert.Equal(t, "alice", p.Active("ws1"))
	assert.Equal(t, "bob", p.Active("ws2"))
}
