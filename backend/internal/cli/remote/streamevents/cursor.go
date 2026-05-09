// Package streamevents holds the cursor + subscription primitives the
// CLI uses to consume worker `WatchEvents` streams. It is shared
// between `agent messages --follow` (single-tab consumer) and `events
// --include agent,terminal` (multi-worker fan-out consumer) so the
// resume-on-reconnect logic only lives in one place.
//
// `WatchEvents` differs from `WatchWorkspacePrivateEvents`: its
// request pins a closed list of `(agent_id, after_seq)` and
// `(terminal_id, after_offset)` entries, and the worker only delivers
// events for those specific tabs. New tabs that appear after the
// subscription is live are silently dropped. So any caller that
// outlives the snapshot it subscribed under has to cancel and
// re-subscribe with an updated entry list — and to be lossless across
// that gap, the new request must carry the per-tab cursor forward.
// That cursor map is what `AgentCursor` and `TerminalCursor` own.
package streamevents

import (
	"sort"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Cursor records a monotonic int64 value per id (agent seq, terminal
// end_offset). Updates are monotonic — a lower value does not regress
// the counter, so out-of-order frame delivery (rare but possible
// across a reconnect) doesn't accidentally rewind the resume point.
// Concrete instantiations (AgentCursor, TerminalCursor) supply a
// builder to turn `(id, value)` pairs into the proto entry the worker
// expects on resubscribe.
type Cursor[Entry any] struct {
	mu         sync.Mutex
	values     map[string]int64
	buildEntry func(id string, value int64) Entry
}

// NewCursor returns an empty cursor. `buildEntry` is invoked once per
// entry in Snapshot to convert the stored value into the proto entry
// shape the worker expects.
func NewCursor[Entry any](buildEntry func(id string, value int64) Entry) *Cursor[Entry] {
	return &Cursor[Entry]{
		values:     map[string]int64{},
		buildEntry: buildEntry,
	}
}

// Track records that the cursor should remember an entry for id even
// before any events arrive (e.g. to seed the entry list on first
// subscribe). Initial value is `initial` unless overridden. Subsequent
// Update calls move the cursor forward as expected.
func (c *Cursor[Entry]) Track(id string, initial int64) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[id]; !ok {
		c.values[id] = initial
	}
}

// Update records value for id iff it advances the counter. Returns
// the cursor value after the update (always the higher of the stored
// value and the input).
func (c *Cursor[Entry]) Update(id string, value int64) int64 {
	if id == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.values[id]
	if !ok || value > cur {
		c.values[id] = value
		return value
	}
	return cur
}

// Get returns the current cursor for id, or 0 if untracked.
func (c *Cursor[Entry]) Get(id string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[id]
}

// Reset drops the cursor for id. Used when the id leaves the snapshot
// (e.g. after a tab close + open of an unrelated tab) so the next
// time it appears we start from a fresh state, not stale state.
func (c *Cursor[Entry]) Reset(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, id)
}

// Snapshot returns the cursor as a sorted entry list ready to embed
// in a fresh WatchEventsRequest. IDs absent from `restrict` are
// excluded; nil `restrict` means "every tracked id". IDs present in
// `restrict` that aren't yet tracked are seeded at value 0 so the
// resubscribe still asks for them (the "new tab on a watched worker"
// case). Sorting is for determinism in tests; the worker doesn't care
// about entry order.
func (c *Cursor[Entry]) Snapshot(restrict map[string]struct{}) []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	type pair struct {
		id    string
		value int64
	}
	var pairs []pair
	if restrict == nil {
		pairs = make([]pair, 0, len(c.values))
		for id, v := range c.values {
			pairs = append(pairs, pair{id: id, value: v})
		}
	} else {
		// Iterating `restrict` keeps `restrict`-but-not-tracked ids seeded
		// at zero in a single pass — the prior implementation built a
		// `known` set to detect those after the fact.
		pairs = make([]pair, 0, len(restrict))
		for id := range restrict {
			pairs = append(pairs, pair{id: id, value: c.values[id]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].id < pairs[j].id })
	out := make([]Entry, len(pairs))
	for i, p := range pairs {
		out[i] = c.buildEntry(p.id, p.value)
	}
	return out
}

// AgentCursor records the highest agent-message seq seen per agent_id.
type AgentCursor = Cursor[*leapmuxv1.WatchAgentEntry]

// NewAgentCursor returns an empty cursor seeded with no agents.
func NewAgentCursor() *AgentCursor {
	return NewCursor(func(id string, seq int64) *leapmuxv1.WatchAgentEntry {
		return &leapmuxv1.WatchAgentEntry{AgentId: id, AfterSeq: seq}
	})
}

// TerminalCursor records the highest cumulative `end_offset` seen per
// terminal_id. Behaves identically to AgentCursor, but the proto
// contract for terminals (`backend/internal/worker/service/terminal.go`)
// allows the worker to silently treat an `after_offset` past the
// retention window or larger than the live counter as 0 — so cursor
// resets aren't a CLI-side error, they're a normal state-rollover
// event the consumer must handle. The Subscription layer flags those
// frames via `is_snapshot=true`.
type TerminalCursor = Cursor[*leapmuxv1.WatchTerminalEntry]

// NewTerminalCursor returns an empty cursor.
func NewTerminalCursor() *TerminalCursor {
	return NewCursor(func(id string, off int64) *leapmuxv1.WatchTerminalEntry {
		return &leapmuxv1.WatchTerminalEntry{TerminalId: id, AfterOffset: off}
	})
}
