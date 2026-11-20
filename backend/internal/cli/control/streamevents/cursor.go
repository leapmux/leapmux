// Package streamevents supplies cursors and subscriptions for worker WatchEvents streams.
// `agent messages --follow` resumes through its cursors. `agent wait`, `agent send --wait`
// and `events` watch in notify mode through its subscriptions, which need no cursor.
// Each request lists the exact agents and terminals that the caller wants.
// Update replaces that interest on the existing stream.
// An unchanged FULL agent keeps its replay identity and fixed replay cursor.
// A fresh stream uses the current cursor and a new replay identity.
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

// Advance records value for id iff it STRICTLY advances the counter, and reports
// whether it did. It folds a dedup's "have I already forwarded this seq?" read and
// the cursor write into ONE locked operation, so two goroutines racing the same
// id+value (e.g. a late frame from a torn-down stream overlapping a reconnect's
// replay of the same seq) can't both observe "not yet seen" and forward a duplicate
// -- exactly one sees advanced=true. A separate Get-then-Update has a window between
// the two locks where both readers see the stale value.
func (c *Cursor[Entry]) Advance(id string, value int64) bool {
	_, advanced := c.advanceFrom(id, value)
	return advanced
}

// advanceFrom returns the prior cursor and advances it in the same operation.
// A terminal uses the prior offset to trim the part of a chunk it has seen.
func (c *Cursor[Entry]) advanceFrom(id string, value int64) (int64, bool) {
	if id == "" {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.advanceLocked(id, value)
}

func (c *Cursor[Entry]) advanceLocked(id string, value int64) bool {
	cur, ok := c.values[id]
	if !ok || value > cur {
		c.values[id] = value
		return cur, true
	}
	return cur, false
}

// replace sets the absolute offset of a replacement snapshot. A new PTY can
// report a lower offset than the terminal the snapshot replaced.
func (c *Cursor[Entry]) replace(id string, value int64) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[id] = value
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

// AgentCursor records the highest delivered sequence and fences ahead-of-cursor
// retractions. An older replay page can have read those rows before deletion.
type AgentCursor struct {
	*Cursor[*leapmuxv1.WatchAgentEntry]
	retracted map[string]map[int64]struct{}
}

// NewAgentCursor returns an empty cursor seeded with no agents.
func NewAgentCursor() *AgentCursor {
	return &AgentCursor{
		Cursor:    NewCursor(AgentWatchEntry),
		retracted: make(map[string]map[int64]struct{}),
	}
}

func (c *AgentCursor) recordRetraction(id string, sequences []int64) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fence := c.retracted[id]
	for _, sequence := range sequences {
		if sequence <= 0 || sequence <= c.values[id] {
			continue
		}
		if fence == nil {
			fence = make(map[int64]struct{})
			c.retracted[id] = fence
		}
		fence[sequence] = struct{}{}
	}
}

// Advance admits a new message unless a retraction already removed it.
func (c *AgentCursor) Advance(id string, value int64) bool {
	if id == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fence := c.retracted[id]
	if _, deleted := fence[value]; deleted || !c.advanceLocked(id, value) {
		return false
	}
	for sequence := range fence {
		if sequence < value {
			delete(fence, sequence)
		}
	}
	if len(fence) == 0 {
		delete(c.retracted, id)
	}
	return true
}

// Reset drops the cursor and outstanding retractions for one agent.
func (c *AgentCursor) Reset(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, id)
	delete(c.retracted, id)
}

// IsResumeCursor reports whether `seq` names a real resume point. Message seqs are
// assigned from 1, so a positive seq is "resume after this" while seq <= 0 means
// nothing has been observed yet (no resume point). The single home for the threshold
// the subscribe path (AgentWatchEntry: LATEST vs AFTER_CURSOR) and the --follow
// backlog drain (drainFetch: OLDEST vs AFTER) both branch on, so the "what counts as
// a resume cursor" rule can't drift between them.
func IsResumeCursor(seq int64) bool {
	return seq > 0
}

// AgentWatchEntry maps a per-agent resume cursor (the highest seq seen, or 0 when
// nothing has been seen yet) to the WatchEvents replay request: a non-resume cursor
// (IsResumeCursor false) maps to a fresh LATEST subscription (tail from the most
// recent page), while a real resume cursor maps to AFTER_CURSOR resume from that seq.
// The wire entry is explicit either way -- no 0/sign overload. Shared by the cursor's
// Snapshot builder and the `agent messages --follow` reconnect loop so the mapping
// lives in one place.
func AgentWatchEntry(id string, seq int64) *leapmuxv1.WatchAgentEntry {
	if !IsResumeCursor(seq) {
		return &leapmuxv1.WatchAgentEntry{
			AgentId: id,
			Replay:  leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			Mode:    leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}
	}
	return &leapmuxv1.WatchAgentEntry{
		AgentId:   id,
		Replay:    leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR,
		CursorSeq: seq,
		Mode:      leapmuxv1.WatchMode_WATCH_MODE_FULL,
	}
}

// TerminalCursor records the expected end offset of each terminal. It
// advances through live chunks and adopts the offset of a replacement
// snapshot, including a lower offset after a restart.
type TerminalCursor = Cursor[*leapmuxv1.WatchTerminalEntry]

// NewTerminalCursor returns an empty cursor.
func NewTerminalCursor() *TerminalCursor {
	return NewCursor(func(id string, off int64) *leapmuxv1.WatchTerminalEntry {
		return &leapmuxv1.WatchTerminalEntry{
			TerminalId:  id,
			AfterOffset: off,
			Mode:        leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}
	})
}
