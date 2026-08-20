package service

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// registryCache is the shared mechanics for a per-agent in-memory registry
// mirror backed by a DB table (agent_todos and agent_background_tasks). Both
// registries need the same cold-start seed, the same snapshot-for-broadcast
// copy, the same linear index lookup, and the same cap-driven eviction of the
// oldest finished row. The type-specific parts (the list query, the row-to-item
// projection, the key/finished/seq extractors, and the delete-by-key query) are
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
	// listRows loads up to `limit` persisted rows for `ownerID` in one cap pool,
	// newest first, projecting each into a stored item plus its persisted seq.
	// `bucket` is "" for a single-pool registry (bucketOf nil).
	listRows func(ctx context.Context, ownerID, bucket string, limit int32) ([]seedEntry[T], error)
	// reclaimFinishedBelowSeq deletes the pool's FINISHED rows older than `seq` --
	// the surplus a seed window leaves behind. Optional: nil skips the pass.
	reclaimFinishedBelowSeq func(ctx context.Context, ownerID, bucket string, seq int64) error
	// keyOf extracts the registry key (row_key / task id) from a stored row.
	keyOf func(T) string
	// setKey sets the registry key on a stored row (in place) for a rename.
	setKey func(*T, string)
	// isFinished reports whether a stored row is final (eligible for eviction).
	isFinished func(T) bool
	// deleteByKey deletes the persisted row for `key` under `ownerID`.
	deleteByKey func(ctx context.Context, ownerID string, key string) error
	// retainInStore reports whether a row must SURVIVE eviction in the store.
	// Eviction then drops it from the capped display cache only. Optional: nil
	// means every evicted row is deleted.
	//
	// The cap is a DISPLAY bound, not a storage bound. A background-task row
	// that carries a child transcript is also the only index from that child id
	// back to (owner, row_key), and the child `agents` row outlives the registry
	// delete -- so deleting the row makes a live subagent permanently
	// unmessageable and uninterruptible. Retention costs one small row per
	// transcript, and the row cascades away with the root agent exactly when the
	// transcript does.
	retainInStore func(T) bool
	// cap is the maximum number of rows the registry holds IN ONE POOL. With
	// bucketOf nil there is a single pool, so this is the whole registry.
	cap int32
	// bucketOf groups rows into independent cap pools, so a burst of one group
	// cannot evict another's rows. Nil means one pool for everything.
	bucketOf func(T) string
	// buckets names every cap pool, so the cold-start seed can load each one to
	// its own cap. Nil/empty means a single pool, seeded with bucket "".
	buckets []string
	// label is used in the eviction error context (e.g. "agent_todos").
	label string
}

// bucket returns the cap pool a row belongs to ("" when the registry has only
// one pool).
func (c *registryCache[T]) bucket(row T) string {
	if c.ops.bucketOf == nil {
		return ""
	}
	return c.ops.bucketOf(row)
}

// inBucket reports whether a row belongs to the named pool.
func (c *registryCache[T]) inBucket(row T, bucket string) bool {
	return c.bucket(row) == bucket
}

// ensureSeededLocked loads the owner's existing rows from the DB on first touch.
// On failure the cache stays unseeded so a later call retries. Caller must hold c.Mu.
// Seeds PER POOL, each to its own cap, because the registry caps each pool
// independently. One global window has the wrong shape: an owner whose newest
// `cap` rows all belong to one pool seeds every other pool EMPTY.
//
// Each pool then reclaims the finished rows its window left behind. Eviction
// only ever touches a row the cache HOLDS, so without this pass a surplus row
// below the window is neither loaded nor deleted -- invisible in the sidebar and
// growing without limit in the DB. A registry that retains rows past the cap
// (see ops.retainInStore) must spare those same rows here, in the reclaim query
// itself, because that surplus is the point: a retained row is an index the
// display list no longer shows. A reclaim failure is logged, not
// returned: the cache is correctly seeded either way, and refusing to seed over
// it would cost the registry its rows.
func (c *registryCache[T]) ensureSeededLocked(ctx context.Context, ownerID string) error {
	if c.seeded {
		return nil
	}
	buckets := c.ops.buckets
	if len(buckets) == 0 {
		buckets = []string{""}
	}
	// listRows returns the NEWEST rows first (see
	// ListAgentBackgroundTasksByKindNewestFirst), so a LIMIT keeps what the cap
	// is meant to keep.
	var loaded []seedEntry[T]
	for _, bucket := range buckets {
		entries, err := c.ops.listRows(ctx, ownerID, bucket, c.ops.cap)
		if err != nil {
			return err
		}
		if c.ops.reclaimFinishedBelowSeq != nil && int32(len(entries)) == c.ops.cap {
			// The window is full, so anything older than its oldest member is
			// surplus. Reclaim only when it IS full: a short window means the
			// pool already fits, and there is nothing behind it.
			oldest := entries[len(entries)-1].seq
			if err := c.ops.reclaimFinishedBelowSeq(ctx, ownerID, bucket, oldest); err != nil {
				slog.Warn("reclaim registry surplus", "registry", c.ops.label, "owner", ownerID, "pool", bucket, "error", err)
			}
		}
		loaded = append(loaded, entries...)
	}
	// Ascending seq order, which is what every reader -- snapshot,
	// eviction-by-slice-order, the sidebar -- assumes. Sorted rather than
	// reversed per pool: the pools interleave in seq, so concatenating their
	// reversed windows would not be ordered overall.
	slices.SortFunc(loaded, func(a, b seedEntry[T]) int { return cmp.Compare(a.seq, b.seq) })
	c.Rows = make([]T, len(loaded))
	var maxSeq int64
	for i, e := range loaded {
		c.Rows[i] = e.item
		if e.seq > maxSeq {
			maxSeq = e.seq
		}
	}
	// Eviction physically deletes the oldest finished row, leaving the remaining
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

// evictOldestFinishedInBucketLocked removes the first finished row (by slice
// order) of ONE cap pool, so making room for a shell row never drops a
// subagent's. Pass "" for a single-pool registry (bucketOf nil), where every row
// shares one pool. RETURNS the evicted row, so a caller that logs it reads the
// row this method actually removed rather than re-running an equivalent scan of
// its own -- two expressions of one predicate that agree only while nobody edits
// either. Returns ok=false (no error) when the pool holds no finished row.
// Caller must hold c.Mu.
//
// There is deliberately NO unscoped wrapper: with a bucketed registry, bucket ""
// matches nothing, so a wrapper that hardcoded it would evict nothing and report
// "no finished row" without an error or a log. Naming the pool is the caller's.
func (c *registryCache[T]) evictOldestFinishedInBucketLocked(ctx context.Context, ownerID, bucket string) (T, bool, error) {
	return c.evictFirstMatchLocked(ctx, ownerID, func(r T) bool {
		return c.inBucket(r, bucket) && c.ops.isFinished(r)
	})
}

// evictOldestInBucketLocked removes the first row (by slice order) of ONE cap
// pool whatever its status, for an insert into a pool that holds no finished row
// at all. The cap is a bound on the DISPLAY list, so the oldest entry leaves it
// even while it runs; ops.retainInStore decides whether the persisted row goes
// with it. Same bucket and return contract as
// evictOldestFinishedInBucketLocked. Caller must hold c.Mu.
func (c *registryCache[T]) evictOldestInBucketLocked(ctx context.Context, ownerID, bucket string) (T, bool, error) {
	return c.evictFirstMatchLocked(ctx, ownerID, func(r T) bool { return c.inBucket(r, bucket) })
}

// evictFirstMatchLocked evicts the first row the predicate accepts, or reports
// ok=false when none matches. Caller must hold c.Mu.
func (c *registryCache[T]) evictFirstMatchLocked(ctx context.Context, ownerID string, match func(T) bool) (T, bool, error) {
	evictIdx := slices.IndexFunc(c.Rows, match)
	if evictIdx < 0 {
		var zero T
		return zero, false, nil
	}
	return c.evictAtLocked(ctx, ownerID, evictIdx)
}

// evictAtLocked removes the row at evictIdx from the capped display cache,
// returning the row it removed. The persisted row goes with it UNLESS
// ops.retainInStore keeps it: eviction is a display bound, and a retained row
// stays addressable by a point lookup on its key. Caller must hold c.Mu.
func (c *registryCache[T]) evictAtLocked(ctx context.Context, ownerID string, evictIdx int) (T, bool, error) {
	evicted := c.Rows[evictIdx]
	if c.ops.retainInStore == nil || !c.ops.retainInStore(evicted) {
		key := c.ops.keyOf(evicted)
		if err := c.ops.deleteByKey(ctx, ownerID, key); err != nil {
			var zero T
			return zero, false, fmt.Errorf("evict %s: %w", c.ops.label, err)
		}
	}
	c.Rows = slices.Delete(c.Rows, evictIdx, evictIdx+1)
	return evicted, true, nil
}

// dropRowLocked removes the cached row at key WITHOUT touching the DB, for a
// caller that already deleted it there. Returns false when no row exists at key.
// Caller must hold c.Mu.
func (c *registryCache[T]) dropRowLocked(key string) bool {
	idx := c.indexOf(key)
	if idx < 0 {
		return false
	}
	c.Rows = slices.Delete(c.Rows, idx, idx+1)
	return true
}

// renameRowKeyLocked re-keys the cached row from oldKey to newKey in place,
// preserving its position, status, and child linkage. Returns false when no row
// exists at oldKey (or newKey is empty) so the caller can no-op. A row already
// present at newKey is dropped so the renamed row stays the single entry for
// that key; the DB rejects that collision outright under its PRIMARY KEY, so
// RenameBackgroundTask resolves it before it calls here and this branch is the
// cache's own consistency guard. Caller must hold c.Mu.
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

// atCapForBucket reports whether one cap pool is full, so an insert into that
// pool knows it must evict before appending. Pass "" for a single-pool registry
// (bucketOf nil), where every row shares one pool. Caller must hold c.Mu.
func (c *registryCache[T]) atCapForBucket(bucket string) bool {
	n := 0
	for _, r := range c.Rows {
		if c.inBucket(r, bucket) {
			n++
		}
	}
	return n >= int(c.ops.cap)
}
