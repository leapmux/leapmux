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

// registryRetention lets a registry keep a row in the store after the display
// cap evicts it from the cache. The cap is then a bound on what the sidebar
// SHOWS, not on what the registry indexes.
//
// The background-task registry needs this: a row that carries a child
// transcript is the only index from that child agent id back to
// (owner, row_key), and the child `agents` row outlives the registry delete --
// so deleting the row leaves a live subagent permanently unmessageable and
// uninterruptible. Retention costs one small row per transcript, and the row
// cascades away with the root agent exactly when the transcript does.
//
// The two halves are ONE field because neither works alone. Keeping a row that
// nothing can read back makes it unreachable, which is the bug retention exists
// to fix; and loading a row that eviction deleted finds nothing. Setting both
// or neither is therefore the only correct choice, and a single pointer is what
// makes it the only available one.
type registryRetention[T any] struct {
	// keep reports whether a row must SURVIVE eviction in the store. Eviction
	// then drops it from the capped display cache only.
	keep func(T) bool
	// load reads a persisted row back by key, so an operation on a row the cache
	// no longer holds still finds it. found=false for a row that does not exist.
	load func(ctx context.Context, ownerID, key string) (row T, found bool, err error)
	// reseq gives a re-admitted row a new seq, so the display list and the
	// cold-start seed keep ONE ordering key. Without it a re-admitted row sat at
	// the end of the list in memory while its stored seq still placed it outside
	// the next seed's window.
	reseq func(ctx context.Context, ownerID, key string, seq int64) error
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
	// retention makes some rows outlive the display cap in the store. Optional:
	// nil means the cap is a storage bound too, and every evicted row is deleted.
	retention *registryRetention[T]
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
// (see ops.retention) must spare those same rows here, in the reclaim query
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
//
// This answers "is the row DISPLAYED", which is not the same question as "does
// the row exist" for a registry with retention. A mutation must ask
// rowIndexLocked instead; indexOf is for a caller that genuinely means the
// display list.
func (c *registryCache[T]) indexOf(key string) int {
	return slices.IndexFunc(c.Rows, func(r T) bool { return c.ops.keyOf(r) == key })
}

// findRowLocked resolves the row at key WITHOUT changing the display list. It
// reports the row, its display index, and whether a row exists at all:
//
//   - found=false, idx=-1: no row exists anywhere.
//   - found=true, idx>=0: the display cache holds it, and idx addresses it.
//   - found=true, idx=-1: the row is RETAINED in the store but left the display
//     list. A caller that means to WRITE calls admitRowLocked to put it back.
//
// Caller must hold c.Mu.
//
// Every mutation resolves through here, and that is what keeps retention
// honest. The cap made the cache a partial view of the table, so a mutation
// that read indexOf alone had a second reason to see -1 -- the row is retained,
// just not displayed -- and it could not tell that apart from "no such row".
// Each applier then dropped its write: a close left the row Running with its
// transcript unended, a status update went nowhere, a revive never reopened the
// row, and a replayed running upsert took the retained row for a new one and
// resurrected it. One lookup for all of them, so no applier can forget the case.
//
// The read and the re-admission are SEPARATE because a mutation decides between
// them. Most appliers guard first and write second, and a guard that stops the
// write must leave the display list alone: admitting there evicts a displayed
// row -- and deletes an unlinked one from the table -- to make space for a write
// that never happens, and the applier then reports changed=false, so no
// broadcast tells the client its list moved.
func (c *registryCache[T]) findRowLocked(ctx context.Context, ownerID, key string) (T, int, bool, error) {
	if idx := c.indexOf(key); idx >= 0 {
		return c.Rows[idx], idx, true, nil
	}
	var zero T
	if c.ops.retention == nil {
		return zero, -1, false, nil
	}
	row, found, err := c.ops.retention.load(ctx, ownerID, key)
	if err != nil {
		return zero, -1, false, fmt.Errorf("load retained %s row %s/%s: %w", c.ops.label, ownerID, key, err)
	}
	if !found {
		return zero, -1, false, nil
	}
	return row, -1, true, nil
}

// admitRowLocked returns a retained row to the display list and reports its new
// index. Call it only at the point where a mutation commits to a write, because
// it evicts to keep the pool at its cap. Caller must hold c.Mu.
//
// The row goes to the END of the display list, and the pool evicts to stay at
// its cap first. Both follow from what the list is for: a row that a mutation
// just touched is the most recent activity in the registry, so it is the last
// thing that should leave the list again.
//
// The stored seq moves with it (ops.retention.reseq), because the position in
// this slice and the seq are ONE ordering key, not two. ensureSeededLocked
// states the rule -- ascending seq is what the snapshot, the eviction by slice
// order, and the sidebar all assume -- and a re-admission that reordered only
// the slice broke it in both directions: eviction stopped picking the oldest
// row, and the next cold seed (newest rows by seq) dropped the re-admitted row
// again, so a subagent a revive had just reopened vanished from the sidebar on
// the next worker restart.
func (c *registryCache[T]) admitRowLocked(ctx context.Context, ownerID string, row T) (int, error) {
	if _, _, err := c.makeRoomLocked(ctx, ownerID, c.bucket(row)); err != nil {
		return -1, err
	}
	if c.ops.retention != nil && c.ops.retention.reseq != nil {
		if err := c.ops.retention.reseq(ctx, ownerID, c.ops.keyOf(row), c.nextSeq); err != nil {
			return -1, fmt.Errorf("resequence %s row %s: %w", c.ops.label, c.ops.keyOf(row), err)
		}
		c.nextSeq++
	}
	c.Rows = append(c.Rows, row)
	return len(c.Rows) - 1, nil
}

// deleteRowLocked removes the row at key from the display list, and from the
// store unless ops.retention keeps it. Reports whether a displayed row left the
// list. Caller must hold c.Mu.
//
// This is the ONE delete a Go caller reaches, so "a retained row survives a
// delete" cannot be forgotten at a new call site: eviction and an explicit
// delete now ask ops.retention.keep through the same code. The seed-time
// reclaim is the one delete that cannot route here -- it is a set-based
// statement over rows the cache never loaded -- so it mirrors the rule in its
// own WHERE clause instead.
func (c *registryCache[T]) deleteRowLocked(ctx context.Context, ownerID, key string) (bool, error) {
	row, idx, found, err := c.findRowLocked(ctx, ownerID, key)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if err := c.dropStoredRowLocked(ctx, ownerID, row); err != nil {
		return false, fmt.Errorf("delete %s: %w", c.ops.label, err)
	}
	if idx < 0 {
		return false, nil
	}
	c.Rows = slices.Delete(c.Rows, idx, idx+1)
	return true, nil
}

// makeRoomLocked frees one slot in a full cap pool so an insert keeps the
// display list at its bound, and does nothing when the pool has room. It owns
// the whole eviction preference: the oldest FINISHED row first, and the oldest
// row of any status when the pool holds none. RETURNS the evicted row (ok=false
// when nothing was evicted), so a caller can report which case it got by testing
// isFinished on it rather than by re-deriving the preference. Caller must hold
// c.Mu.
func (c *registryCache[T]) makeRoomLocked(ctx context.Context, ownerID, bucket string) (T, bool, error) {
	if !c.atCapForBucket(bucket) {
		var zero T
		return zero, false, nil
	}
	evicted, ok, err := c.evictOldestFinishedInBucketLocked(ctx, ownerID, bucket)
	if err != nil || ok {
		return evicted, ok, err
	}
	return c.evictOldestInBucketLocked(ctx, ownerID, bucket)
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
// even while it runs; ops.retention decides whether the persisted row goes with
// it. Same bucket and return contract as
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
// ops.retention keeps it: eviction is a display bound, and a retained row stays
// reachable through rowIndexLocked. Caller must hold c.Mu.
func (c *registryCache[T]) evictAtLocked(ctx context.Context, ownerID string, evictIdx int) (T, bool, error) {
	evicted := c.Rows[evictIdx]
	if err := c.dropStoredRowLocked(ctx, ownerID, evicted); err != nil {
		var zero T
		return zero, false, fmt.Errorf("evict %s: %w", c.ops.label, err)
	}
	c.Rows = slices.Delete(c.Rows, evictIdx, evictIdx+1)
	return evicted, true, nil
}

// dropStoredRowLocked deletes the persisted row unless ops.retention keeps it,
// and is the ONE place that asks the question. A registry without retention
// deletes every row. Caller must hold c.Mu.
func (c *registryCache[T]) dropStoredRowLocked(ctx context.Context, ownerID string, row T) error {
	if c.ops.retention != nil && c.ops.retention.keep(row) {
		return nil
	}
	return c.ops.deleteByKey(ctx, ownerID, c.ops.keyOf(row))
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
