package crdt

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Journal is the persistence interface the manager depends on. The
// concrete implementation lives in the hub service package and binds
// to a single org's DB-level transactional boundary.
type Journal interface {
	// LoadState returns the persisted org state plus the tail of
	// batches after compaction_watermark. nil state means "first
	// boot — no state row yet"; the tail is empty in that case.
	LoadState(ctx context.Context, orgID string) (state *leapmuxv1.OrgCrdtState, tail []*leapmuxv1.OpBatch, err error)

	// CommitBatch atomically writes the batch to org_op_batches, the
	// dedup row to org_recent_batch_ids, and the index-view diff to
	// workspace_tab_owned / workspace_tab_rendered. All three writes
	// land inside a single DB transaction; on rollback the manager's
	// in-memory state is NOT advanced (the caller checks err).
	CommitBatch(ctx context.Context, c CommitBatch) error

	// LookupRecentBatchID returns a previously-committed batch_id row
	// within the dedup window. Returns nil if absent.
	LookupRecentBatchID(ctx context.Context, orgID, batchID string) (*RecentBatchRecord, error)

	// AdvanceEpoch bumps current_epoch + epoch_started_at without
	// rewriting the state_payload.
	AdvanceEpoch(ctx context.Context, orgID string, epoch int64, startedAt time.Time) error

	// CompactBatch atomically rewrites org_state.state_payload (with
	// the new compaction_watermark) AND moves about-to-be-deleted
	// batches into org_recent_batch_ids AND deletes the journal rows
	// whose batch's last canonical HLC ≤ watermark. The manager hands
	// the freshly-compacted state and the dedup-row inserts; on
	// success the caller advances its in-memory compaction_watermark.
	CompactBatch(ctx context.Context, c CompactBatch) error

	// CleanupExpiredRecentBatchIDs deletes dedup rows past their TTL.
	// Periodic; doesn't need transactional accuracy.
	CleanupExpiredRecentBatchIDs(ctx context.Context, before time.Time) (int64, error)
}

// CommitBatch carries the inputs to Journal.CommitBatch.
type CommitBatch struct {
	OrgID       string
	Batch       *leapmuxv1.OpBatch
	PrincipalID string
	Epoch       int64
	DedupRow    RecentBatchRecord
	IndexDiff   IndexDiff
}

// CompactBatch carries the inputs to Journal.CompactBatch.
type CompactBatch struct {
	State       *leapmuxv1.OrgCrdtState
	DropThrough *leapmuxv1.HLC
}

// RecentBatchRecord is the wire shape of a dedup-table row. One row per
// committed batch. CanonicalFirstHLC is the HLC of the batch's first op;
// the per-op HLCs of a retry response are reconstructed as
// (canon.physical, canon.logical+i, canon.client) for i in [0, OpCount).
type RecentBatchRecord struct {
	OrgID             string
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
	OrgID   string
	OpType  LifecycleOpType
	Payload []byte
}

// LifecycleOutboxReader is the manager's view onto the outbox table.
type LifecycleOutboxReader interface {
	ListPendingLifecycleOutbox(ctx context.Context, orgID string) ([]LifecycleOutboxRow, error)
	MarkLifecycleOutboxConsumed(ctx context.Context, id int64, consumedAt time.Time) error
}
