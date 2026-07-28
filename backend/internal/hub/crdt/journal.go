package crdt

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Journal is the persistence interface the manager depends on. The
// concrete implementation lives in the hub service package and binds
// to a single user's DB-level transactional boundary.
type Journal interface {
	// LoadState returns the persisted user state plus the tail of
	// batches after compaction_watermark. nil state means "first
	// boot — no state row yet"; the tail is empty in that case.
	LoadState(ctx context.Context, userID string) (state *leapmuxv1.UserCrdtState, tail []*leapmuxv1.OpBatch, err error)

	// CommitBatch atomically writes the batch to user_op_batches, the
	// dedup row to user_recent_batch_ids, and the index-view diff to
	// workspace_tab_owned / workspace_tab_rendered. All three writes
	// land inside a single DB transaction; on rollback the manager's
	// in-memory state is NOT advanced (the caller checks err).
	CommitBatch(ctx context.Context, c CommitBatch) error

	// LookupRecentBatchID returns a previously-committed batch_id row
	// within the dedup window. Returns nil if absent.
	LookupRecentBatchID(ctx context.Context, userID, batchID string) (*RecentBatchRecord, error)

	// AdvanceEpoch bumps current_epoch + epoch_started_at without
	// rewriting the state_payload.
	AdvanceEpoch(ctx context.Context, userID string, epoch int64, startedAt time.Time) error

	// CompactBatch atomically rewrites user_state.state_payload (with
	// the new compaction_watermark) AND deletes the user_op_batches rows
	// whose batch's last canonical HLC ≤ watermark. The manager hands
	// the freshly-compacted state and that watermark; on success the
	// caller advances its in-memory compaction_watermark.
	//
	// It does NOT touch user_recent_batch_ids, and compaction does not
	// narrow the retry-idempotence window. Dedup rows are written at
	// COMMIT time (see CommitBatch) with their own expires_at, and are
	// removed only by CleanupExpiredRecentBatchIDs below -- so a client
	// retrying a batch across a compaction boundary still hits its dedup
	// row. See Manager.maybeCompact for the same statement from the other side.
	CompactBatch(ctx context.Context, c CompactBatch) error

	// CleanupExpiredRecentBatchIDs deletes dedup rows past their TTL.
	// Periodic; doesn't need transactional accuracy.
	CleanupExpiredRecentBatchIDs(ctx context.Context, before time.Time) (int64, error)
}

// CommitBatch carries the inputs to Journal.CommitBatch. UserID, PrincipalID
// and Epoch are the whole commit's context: every row it writes -- the journal
// row, the dedup row, and the index-view rows -- belongs to that one tenant,
// principal and epoch, so they are stated once here rather than repeated per
// row.
type CommitBatch struct {
	UserID      string
	Batch       *leapmuxv1.OpBatch
	PrincipalID string
	Epoch       int64
	Dedup       DedupEntry
	IndexDiff   IndexDiff
}

// DedupEntry is the batch-specific half of a user_recent_batch_ids row: the
// columns that vary per batch, with none of the enclosing commit's context.
//
// It is deliberately NOT a RecentBatchRecord. That shape also carries UserID,
// Epoch and PrincipalID, which the enclosing CommitBatch already states -- and
// a second copy of a value can only ever agree (buying nothing) or disagree
// (a tenancy bug the journal would have to defend against). RecentBatchRecord
// keeps those fields because it is LookupRecentBatchID's RETURN shape: a row
// read back from the DB stands alone, with no enclosing commit to take them
// from.
type DedupEntry struct {
	BatchID           string
	BodyHash          []byte
	CanonicalFirstHLC *leapmuxv1.HLC
	OpCount           int64
	ExpiresAt         time.Time
}

// CompactBatch carries the inputs to Journal.CompactBatch.
type CompactBatch struct {
	State       *leapmuxv1.UserCrdtState
	DropThrough *leapmuxv1.HLC
}

// RecentBatchRecord is the wire shape of a dedup-table row. One row per
// committed batch. CanonicalFirstHLC is the HLC of the batch's first op;
// the per-op HLCs of a retry response are reconstructed as
// (canon.physical, canon.logical+i, canon.client) for i in [0, OpCount).
type RecentBatchRecord struct {
	UserID            string
	BatchID           string
	BodyHash          []byte
	PrincipalID       string
	CanonicalFirstHLC *leapmuxv1.HLC
	OpCount           int64
	Epoch             int64
	ExpiresAt         time.Time
}

// LifecycleOutboxRow is the wire shape of a single outbox payload the
// manager consumes.
type LifecycleOutboxRow struct {
	ID      int64
	UserID  string
	OpType  LifecycleOpType
	Payload []byte
}

// LifecycleOutboxReader is the manager's view onto the outbox table.
type LifecycleOutboxReader interface {
	ListPendingLifecycleOutbox(ctx context.Context, userID string) ([]LifecycleOutboxRow, error)
	MarkLifecycleOutboxConsumed(ctx context.Context, id int64, consumedAt time.Time) error
}
