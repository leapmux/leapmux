package crdt

import (
	"context"
	"errors"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrDeltaTooLarge is returned by ListBatchesAfter when the post-cursor tail
// exceeds the caller's maxOps or maxBytes budget. The receiver should fall
// back to a full materialized snapshot rather than ship an unbounded delta
// on resume.
var ErrDeltaTooLarge = errors.New("crdt: resume delta exceeds budget")

// ErrResumeCorrupt reports that ListBatchesAfter hit a row whose batch_payload
// or transitions_payload could not be decoded. Like ErrDeltaTooLarge it is a
// RECOVERABLE verdict, not a connect failure: buildResumeDelta branches on it
// via errors.Is and re-enters the full-snapshot FALLBACK path. The offending
// row is also returned in the CorruptRows slice so the caller can log it.
//
// The scan stops rather than skipping the row, because a delta that omits one
// committed batch is indistinguishable on the wire from that batch never having
// existed — while the frames after it still advance the client's watermark past
// it, and the resume cursor is strictly-greater, so no later resume re-requests
// it. One full snapshot is cheap; a permanently diverged client is not.
var ErrResumeCorrupt = errors.New("crdt: resume journal row corrupt")

// ErrBootJournalCorrupt reports the SAME damage as ErrResumeCorrupt found on the
// BOOT scan, where the verdict is the opposite: there is no snapshot to fall
// back to, because the snapshot is what Bootstrap is trying to build.
//
// Two sentinels rather than one because the sentinel IS the verdict. Wrapping a
// boot failure in ErrResumeCorrupt made `errors.Is(err, ErrResumeCorrupt)` true
// for an error whose whole documented meaning is "degrade to a snapshot" — so
// the first handler to grow that arm above registry.Get would serve a snapshot
// built from a state blob missing every op after the bad row, and report a
// successful connect. Nothing branches on it there today; the mislabel is the
// hazard, and it was pinned by a passing test.
var ErrBootJournalCorrupt = errors.New("crdt: boot journal row corrupt")

// Journal is the persistence interface the manager depends on. The
// concrete implementation lives in the hub service package and binds
// to a single user's DB-level transactional boundary.
type Journal interface {
	// LoadState returns the persisted user state plus the tail of
	// batches after compaction_watermark. nil state means "first
	// boot — no state row yet"; the tail is empty in that case.
	LoadState(ctx context.Context, userID string) (state *leapmuxv1.UserCrdtState, tail []*leapmuxv1.OpBatch, err error)

	// ListBatchesAfter returns the journal's op batches (with their persisted
	// per-entity workspace transitions) strictly after the given cursor HLC,
	// paged forward by HLC tuple. It is the resume counterpart to the tail
	// LoadState returns at boot: same underlying scan, just bounded by a
	// client-supplied cursor instead of compaction_watermark. Each ResumeBatch
	// pairs the batch with its BatchTransitions so buildResumeDelta can replay
	// the SAME pre/post stable-visibility classification the live broadcast
	// applies — emitting materialized/removed frames for entities that crossed
	// the subscriber's visibility boundary during the gap, not just the
	// current-workspace-filtered raw ops.
	//
	// maxOps caps the total op count (across all batches); maxBytes caps the
	// sum of batch_payload + transitions_payload sizes. Either budget <= 0
	// disables that ceiling. until, when non-nil, exclusive-caps the scan to
	// batches whose first-op HLC is <= until (the register-time high-water):
	// commits that land after registration (HLC > until) are owned by live
	// broadcast into the just-registered subscriber and must not also appear
	// in the delta. until nil means unbounded (LoadState / boot).
	//
	// If the tail would exceed an enabled ceiling, the scan aborts with
	// ErrDeltaTooLarge so the caller can fall back to a full snapshot rather
	// than ship a giant delta. On ErrDeltaTooLarge the returned slice is
	// incidental (batches decoded before the ceiling was hit) and MUST be
	// discarded — SubscribeWithACL branches on the sentinel and re-enters the
	// full-snapshot path.
	//
	// A row whose batch_payload or transitions_payload cannot be decoded STOPS
	// the scan and returns ErrResumeCorrupt, with that row's id + field + cause
	// in the CorruptRows slice for logging. Like ErrDeltaTooLarge this is a
	// FALLBACK signal, not a connect failure, and the returned batches MUST be
	// discarded: shipping the prefix would omit the corrupt batch while later
	// frames advanced the client past it, permanently. Both payload fields are
	// treated alike — empty transitions are not a safe default, they make
	// filterVisibleOps drop every op in the batch.
	ListBatchesAfter(ctx context.Context, userID string, after, until *leapmuxv1.HLC, maxOps, maxBytes int) (batches []ResumeBatch, corrupt []CorruptRow, err error)

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
	// Transitions is the per-batch AffectedEntities (encoded by
	// AffectedEntitiesToProto) persisted alongside the OpBatch so a resume can
	// replay the SAME pre/post stable-visibility classification the live
	// broadcast applies. Nil is treated as empty (the journal marshals an empty
	// BatchTransitions), so a caller with no transitions still commits a
	// NOT NULL row.
	Transitions *leapmuxv1.BatchTransitions
}

// ResumeBatch pairs a journal op batch with its persisted per-entity workspace
// transitions. ListBatchesAfter yields these so buildResumeDelta has both the
// ops (to filter to the stable-visibility subset) and the transitions (to
// classify each entity as materialized / stable / removed for this subscriber).
type ResumeBatch struct {
	Batch       *leapmuxv1.OpBatch
	Transitions *leapmuxv1.BatchTransitions
}

// CorruptRow names a journal row whose batch_payload or transitions_payload
// could not be decoded. ListBatchesAfter STOPS at such a row and returns it
// alongside ErrResumeCorrupt, so the caller can log the damage and FALLBACK to
// a full snapshot. The scan deliberately does NOT continue past it: a delta
// that omits one committed batch still advances the client's max_hlc past that
// batch, and the resume cursor is strictly-greater, so the client would never
// re-request it — a permanent divergence, versus one cheap full snapshot.
// Field is "batch_payload" or "transitions_payload"; Cause is wrapped via
// wrapResumeCorrupt (carrying ErrResumeCorrupt).
type CorruptRow struct {
	BatchID string
	Field   string
	Cause   error
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
