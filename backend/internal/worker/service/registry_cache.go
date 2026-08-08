package service

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// registryCache is the shared mechanics for a per-agent in-memory registry
// mirror backed by a DB table (agent_todos and agent_background_tasks). Both
// registries need the same cold-start seed, the same snapshot-for-broadcast
// copy, the same linear index lookup, and the same cap-driven eviction of the
// oldest terminal row. The type-specific parts (the list query, the row-to-item
// projection, the key/terminal/seq extractors, and the delete-by-key query) are
// supplied by registryOps so the mechanics live in exactly one place.
//
// The cache is NOT safe for concurrent use by itself; each instance carries its
// own mutex (Mu) and every read-mutate-broadcast cycle that touches Rows must
// hold it.
type registryCache[T any] struct {
	Mu      sync.Mutex
	seeded  bool
	Rows    []T
	nextSeq int64
	ops     registryOps[T]
}

// seedEntry pairs a stored item with its persisted seq, so ensureSeededLocked
// can derive nextSeq from the max without the item type itself carrying a seq
// field (bgtask.Item deliberately omits it).
type seedEntry[T any] struct {
	item T
	seq  int64
}

// registryOps supplies the type-specific behaviour a registryCache needs. It is
// built once per OutputHandler (the closures capture h.queries) and shared by
// every cache instance of that registry type. Both the background-task registry
// (bgTaskOps) and agent_todos (todoOps) supply one.
type registryOps[T any] struct {
	// listRows loads up to `limit` persisted rows for `ownerID`, projecting each
	// into a stored item plus its persisted seq.
	listRows func(ctx context.Context, ownerID string, limit int32) ([]seedEntry[T], error)
	// keyOf extracts the registry key (row_key / task id) from a stored row.
	keyOf func(T) string
	// setKey sets the registry key on a stored row (in place) for a rename.
	setKey func(*T, string)
	// isTerminal reports whether a stored row is terminal (eligible for eviction).
	isTerminal func(T) bool
	// deleteByKey deletes the persisted row for `key` under `ownerID`.
	deleteByKey func(ctx context.Context, ownerID string, key string) error
	// cap is the maximum number of rows the registry holds.
	cap int32
	// label is used in the eviction error context (e.g. "agent_todos").
	label string
}

// ensureSeededLocked loads the owner's existing rows from the DB on first touch.
// On failure the cache stays unseeded so a later call retries. Caller must hold c.Mu.
func (c *registryCache[T]) ensureSeededLocked(ctx context.Context, ownerID string) error {
	if c.seeded {
		return nil
	}
	entries, err := c.ops.listRows(ctx, ownerID, c.ops.cap)
	if err != nil {
		return err
	}
	c.Rows = make([]T, len(entries))
	var maxSeq int64
	for i, e := range entries {
		c.Rows[i] = e.item
		if e.seq > maxSeq {
			maxSeq = e.seq
		}
	}
	// Eviction physically deletes the oldest terminal row, leaving the remaining
	// seqs sparse. A len(rows)+1 start would collide with the highest surviving
	// seq on the next insert, so derive from the actual max.
	c.nextSeq = maxSeq + 1
	c.seeded = true
	return nil
}

// snapshot returns a freshly-allocated copy of the cache's rows. The caller
// (broadcast payload, Load result) owns the returned slice. Caller must hold c.Mu.
func (c *registryCache[T]) snapshot() []T {
	out := make([]T, len(c.Rows))
	copy(out, c.Rows)
	return out
}

// indexOf returns the row index whose key matches, or -1. Caller must hold c.Mu.
func (c *registryCache[T]) indexOf(key string) int {
	return slices.IndexFunc(c.Rows, func(r T) bool { return c.ops.keyOf(r) == key })
}

// evictOldestTerminalLocked removes the first terminal row (by slice order) from
// the cache and DB to make room under the cap. Returns false (no error) when no
// terminal row exists. Caller must hold c.Mu.
func (c *registryCache[T]) evictOldestTerminalLocked(ctx context.Context, ownerID string) (bool, error) {
	evictIdx := slices.IndexFunc(c.Rows, c.ops.isTerminal)
	if evictIdx < 0 {
		return false, nil
	}
	return c.evictAtLocked(ctx, ownerID, evictIdx)
}

// evictAtLocked deletes the row at evictIdx from the cache and DB. Caller must
// hold c.Mu.
func (c *registryCache[T]) evictAtLocked(ctx context.Context, ownerID string, evictIdx int) (bool, error) {
	key := c.ops.keyOf(c.Rows[evictIdx])
	if err := c.ops.deleteByKey(ctx, ownerID, key); err != nil {
		return false, fmt.Errorf("evict %s: %w", c.ops.label, err)
	}
	c.Rows = slices.Delete(c.Rows, evictIdx, evictIdx+1)
	return true, nil
}

// renameRowKeyLocked re-keys the cached row from oldKey to newKey in place,
// preserving its position, status, and child linkage. Returns false when no row
// exists at oldKey (or newKey is empty) so the caller can no-op. A row already
// present at newKey is overwritten (the caller renames only to a freshly-learned
// id that has no row yet). Caller must hold c.Mu.
func (c *registryCache[T]) renameRowKeyLocked(oldKey, newKey string) bool {
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return false
	}
	idx := c.indexOf(oldKey)
	if idx < 0 {
		return false
	}
	// Drop any stale entry at newKey so the renamed row is the single source.
	if newIdx := c.indexOf(newKey); newIdx >= 0 {
		c.Rows = slices.Delete(c.Rows, newIdx, newIdx+1)
		// The delete may have shifted idx; re-resolve.
		idx = c.indexOf(oldKey)
		if idx < 0 {
			return false
		}
	}
	c.ops.setKey(&c.Rows[idx], newKey)
	return true
}

// atCap reports whether the cache holds the maximum number of rows, so an
// insert knows it must evict before appending. Caller must hold c.Mu.
func (c *registryCache[T]) atCap() bool {
	return len(c.Rows) >= int(c.ops.cap)
}
