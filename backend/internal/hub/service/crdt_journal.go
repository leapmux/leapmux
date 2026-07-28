package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/proto"
)

// errBlankTenant is returned by every journal method whose crdt-side
// `userID string` will not mint. This adapter is the boundary at which the
// store's typed owner params are minted: crdt keeps its actor ids string-keyed
// (see internal/audit/typing.go), so the conversion happens here, once per
// call, and refuses rather than writing a row whose owner no ownership
// predicate could ever bind.
//
// Nothing reaches it today. crdt.Registry.Get refuses a blank tenancy key, so a
// Manager's userID is non-empty by construction, and Manager.requireOwnState
// refuses a state payload naming any other tenant, so the payload-derived id
// CompactBatch mints agrees with it. That is precisely why this is an error
// rather than a silent no-op: reaching it means one of those invariants broke,
// and a silent skip would hide that.
var errBlankTenant = errors.New("crdt journal: blank user id; refusing to write a row no ownership predicate could reach")

// crdtJournal adapts store.Store to the crdt.Journal contract. It owns
// the per-batch transaction boundary so the manager's commit step lands
// AppendBatch + InsertRecentBatchID + ApplyDiff atomically.
type crdtJournal struct {
	store store.Store
}

// NewCRDTJournal returns a Journal backed by the supplied store.
func NewCRDTJournal(st store.Store) crdt.Journal {
	return &crdtJournal{store: st}
}

// mintTenant binds a journal method's untyped tenancy key into the owner every
// store call beneath it takes, refusing a blank one with errBlankTenant.
//
// The refusal is the substance here, not the conversion. A blank key is not a
// benign "no tenant": it unwraps to "" and MATCHES every blank-owner row, so
// each entry point has to reject it before the first query rather than let the
// store answer (see userid.OwnerFilter). Naming that rule once keeps the five
// call sites from drifting on which error they raise, though it cannot make
// them remember to call it -- a new journal method still has to, which is why
// the audit in internal/audit, not this helper, is what enforces the guard.
//
// Callers differ only in how many zero values they pair with the error, so this
// returns the error rather than an ok bool.
func mintTenant(userID string) (userid.UserID, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return userid.UserID{}, errBlankTenant
	}
	return owner, nil
}

func (j *crdtJournal) LoadState(ctx context.Context, userID string) (*leapmuxv1.UserCrdtState, []*leapmuxv1.OpBatch, error) {
	owner, err := mintTenant(userID)
	if err != nil {
		return nil, nil, err
	}
	row, err := j.store.UserState().Get(ctx, owner)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("get user_state: %w", err)
	}
	var state *leapmuxv1.UserCrdtState
	if err == nil && row != nil {
		state = &leapmuxv1.UserCrdtState{}
		if uerr := proto.Unmarshal(row.StatePayload, state); uerr != nil {
			return nil, nil, fmt.Errorf("unmarshal state_payload: %w", uerr)
		}
	}
	var watermark *leapmuxv1.HLC
	if state != nil {
		watermark = state.GetCompactionWatermark()
	}
	tail, err := j.listBatchesAfter(ctx, owner, watermark)
	if err != nil {
		return nil, nil, err
	}
	return state, tail, nil
}

func (j *crdtJournal) listBatchesAfter(ctx context.Context, owner userid.UserID, watermark *leapmuxv1.HLC) ([]*leapmuxv1.OpBatch, error) {
	// Page through the journal so a far-behind subscriber cannot load
	// the entire backlog in one slab. The cursor walks forward by the
	// last consumed batch's HLC; we stop once a page comes back
	// short of the limit.
	out := []*leapmuxv1.OpBatch{}
	cur := watermark
	for {
		rows, err := j.store.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID:            owner,
			AfterPhysicalMs:   cur.GetPhysical(),
			AfterLogical:      cur.GetLogical(),
			AfterOriginClient: cur.GetClientId(),
			Limit:             store.CRDTBatchPageLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("list user_op_batches after watermark: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			batch := &leapmuxv1.OpBatch{}
			if uerr := proto.Unmarshal(r.BatchPayload, batch); uerr != nil {
				return nil, fmt.Errorf("unmarshal user_op_batch %s: %w", r.BatchID, uerr)
			}
			out = append(out, batch)
		}
		if len(rows) < store.CRDTBatchPageLimit {
			break
		}
		last := rows[len(rows)-1]
		cur = &leapmuxv1.HLC{
			Physical: last.PhysicalMs,
			Logical:  last.LastLogical,
			ClientId: last.OriginClient,
		}
	}
	return out, nil
}

func (j *crdtJournal) CommitBatch(ctx context.Context, c crdt.CommitBatch) error {
	// One mint for the committing tenant, minted here rather than per journal
	// row: every row a batch writes -- the journal row, the dedup row and the
	// index-view rows -- belongs to this one transaction's user, which
	// crdt.CommitBatch states exactly once.
	owner, err := mintTenant(c.UserID)
	if err != nil {
		return err
	}
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		payload, err := proto.Marshal(c.Batch)
		if err != nil {
			return fmt.Errorf("marshal batch %s: %w", c.Batch.GetBatchId(), err)
		}
		ops := c.Batch.GetOps()
		opCount := int64(len(ops))
		first := ops[0].GetCanonicalHlc()
		last := ops[opCount-1].GetCanonicalHlc()
		if err := tx.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID:       owner,
			PhysicalMs:   first.GetPhysical(),
			Logical:      first.GetLogical(),
			LastLogical:  last.GetLogical(),
			OriginClient: first.GetClientId(),
			PrincipalID:  c.PrincipalID,
			BatchID:      c.Batch.GetBatchId(),
			BodyHash:     c.Dedup.BodyHash,
			BatchPayload: payload,
			OpCount:      opCount,
			Epoch:        c.Epoch,
		}); err != nil {
			return fmt.Errorf("insert user_op_batch %s: %w", c.Batch.GetBatchId(), err)
		}
		// The dedup row's tenant, principal and epoch come off the commit
		// envelope, not off the entry: it is the same transaction's row.
		d := c.Dedup
		dCanon := d.CanonicalFirstHLC
		if err := tx.UserRecentBatchIDs().Insert(ctx, store.InsertUserRecentBatchIDParams{
			UserID:              owner,
			BatchID:             d.BatchID,
			BodyHash:            d.BodyHash,
			PrincipalID:         c.PrincipalID,
			CanonicalPhysicalMs: dCanon.GetPhysical(),
			CanonicalLogical:    dCanon.GetLogical(),
			CanonicalClient:     dCanon.GetClientId(),
			OpCount:             d.OpCount,
			Epoch:               c.Epoch,
			ExpiresAt:           d.ExpiresAt,
		}); err != nil {
			return fmt.Errorf("insert dedup row %s: %w", d.BatchID, err)
		}
		idx := txTabIndexWriter{tx: tx, owner: owner}
		return crdt.ApplyDiff(ctx, idx, c.IndexDiff)
	})
}

func (j *crdtJournal) LookupRecentBatchID(ctx context.Context, userID, batchID string) (*crdt.RecentBatchRecord, error) {
	// A blank tenant is uniquely dangerous HERE, which is why this refuses
	// rather than letting the store answer. The store maps an unminted owner to
	// ErrNotFound, and this method translates that to crdt.ErrNotFound -- which
	// crdt.Manager.runDedup reads as "no prior commit for this batch id,
	// proceed". A broken tenancy invariant would therefore be indistinguishable
	// from a legitimate dedup miss, and would silently DISABLE retry
	// idempotence (re-applying an already-committed batch) instead of surfacing.
	owner, err := mintTenant(userID)
	if err != nil {
		return nil, err
	}
	row, err := j.store.UserRecentBatchIDs().Get(ctx, owner, batchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, crdt.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &crdt.RecentBatchRecord{
		UserID:            row.UserID,
		BatchID:           row.BatchID,
		BodyHash:          row.BodyHash,
		PrincipalID:       row.PrincipalID,
		CanonicalFirstHLC: &leapmuxv1.HLC{Physical: row.CanonicalPhysicalMs, Logical: row.CanonicalLogical, ClientId: row.CanonicalClient},
		OpCount:           row.OpCount,
		Epoch:             row.Epoch,
		ExpiresAt:         row.ExpiresAt,
	}, nil
}

func (j *crdtJournal) AdvanceEpoch(ctx context.Context, userID string, epoch int64, startedAt time.Time) error {
	owner, err := mintTenant(userID)
	if err != nil {
		return err
	}
	return j.store.UserState().AdvanceEpoch(ctx, store.AdvanceUserEpochParams{
		UserID:         owner,
		Epoch:          epoch,
		EpochStartedAt: startedAt,
		UpdatedAt:      startedAt,
	})
}

func (j *crdtJournal) CompactBatch(ctx context.Context, c crdt.CompactBatch) error {
	// The state payload's own user_id keys both writes below, exactly as it did
	// before the retype -- crdt.Manager.requireOwnState has already refused a
	// payload naming any tenant other than the manager's, so this is that id.
	owner, err := mintTenant(c.State.GetUserId())
	if err != nil {
		return err
	}
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		payload, err := proto.Marshal(c.State)
		if err != nil {
			return fmt.Errorf("marshal state: %w", err)
		}
		now := time.Now()
		if err := tx.UserState().Upsert(ctx, store.UpsertUserStateParams{
			UserID:         owner,
			StatePayload:   payload,
			CurrentEpoch:   c.State.GetCurrentEpoch(),
			EpochStartedAt: c.State.GetEpochStartedAt().AsTime(),
			UpdatedAt:      now,
		}); err != nil {
			return fmt.Errorf("upsert user_state: %w", err)
		}
		if c.DropThrough != nil {
			if err := tx.UserOpBatches().DeleteThrough(ctx, store.DeleteUserOpBatchesThroughParams{
				UserID:              owner,
				ThroughPhysicalMs:   c.DropThrough.GetPhysical(),
				ThroughLogical:      c.DropThrough.GetLogical(),
				ThroughOriginClient: c.DropThrough.GetClientId(),
			}); err != nil {
				return fmt.Errorf("delete user_op_batches through: %w", err)
			}
		}
		return nil
	})
}

func (j *crdtJournal) CleanupExpiredRecentBatchIDs(ctx context.Context, before time.Time) (int64, error) {
	return j.store.UserRecentBatchIDs().DeleteExpired(ctx, before)
}

// txTabIndexWriter is a thin adapter from crdt.TabIndexWriter to the
// transactional store.WorkspaceTabIndexStore. All four methods are
// bulk: crdt.ApplyDiff hands the writer the full per-batch slices in
// one call, and the underlying store chunks internally when the
// backend's parameter limit demands it.
//
// owner is the committing tenant, minted once by CommitBatch. It is the SOLE
// source of the owner column on the UPSERT paths: crdt.TabIndexRow carries no
// owner of its own, so there is nothing for this to disagree with. That is what
// makes "a commit only ever writes its own user's index rows" structural rather
// than data-dependent -- a projected row is derived from state.GetUserId() and
// crdt.Manager.requireOwnState refuses a state payload naming any other tenant,
// so the row's owner was always this one, and dropping the field removes the
// only way for the two to drift.
//
// The DELETE paths are the asymmetry: crdt.TabKey DOES carry an owner, because
// a key names an EXISTING row whose owner is half the primary key. Those bind
// each key's own owner, so one unusable key does not cancel the deletes queued
// for its neighbours.
type txTabIndexWriter struct {
	tx    store.Store
	owner userid.UserID
}

func (w txTabIndexWriter) BulkUpsertOwned(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertOwned(ctx, w.tabParams(rows))
}

func (w txTabIndexWriter) BulkDeleteOwned(ctx context.Context, keys []crdt.TabKey) error {
	storeKeys := tabIndexKeys(keys)
	if len(storeKeys) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteOwned(ctx, storeKeys)
}

func (w txTabIndexWriter) BulkUpsertRendered(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertRendered(ctx, w.tabParams(rows))
}

func (w txTabIndexWriter) BulkDeleteRendered(ctx context.Context, keys []crdt.TabKey) error {
	storeKeys := tabIndexKeys(keys)
	if len(storeKeys) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteRendered(ctx, storeKeys)
}

// tabParams converts a diff's rows to store params, supplying the committing
// tenant as the owner column (a crdt.TabIndexRow carries none).
// store.UpsertRenderedTabParams is an alias of store.UpsertOwnedTabParams (the
// two views share one column set), so the owned and rendered upserts share this
// single conversion instead of repeating it.
func (w txTabIndexWriter) tabParams(rows []crdt.TabIndexRow) []store.UpsertOwnedTabParams {
	params := make([]store.UpsertOwnedTabParams, len(rows))
	for i, row := range rows {
		params[i] = store.UpsertOwnedTabParams{
			UserID:      w.owner,
			WorkspaceID: row.WorkspaceID,
			TabType:     row.TabType,
			TabID:       row.TabID,
			WorkerID:    row.WorkerID,
			TileID:      row.TileID,
			Position:    row.Position,
		}
	}
	return params
}

// tabIndexKeys adapts CRDT tab keys to store keys. It is a pure shape
// conversion, and the mint is part of that shape: a crdt key carries an untyped
// `UserID string`, a store key carries a userid.UserID, and a blank one mints
// to the ZERO UserID rather than being refused here. Blank-owner refusal
// belongs to the store, which applies store.FilterTabIndexKeys at every site
// that binds an owner column (sqlutil.BulkDeleteTabs for sqlite/mysql, the
// postgres adapter directly) and SKIPS the zero keys there. Keeping the rule
// there rather than here means a future non-CRDT caller of BulkDeleteOwned
// inherits it instead of having to know to re-copy it -- and means one unusable
// key never cancels its neighbours, which an early refusal here would.
func tabIndexKeys(keys []crdt.TabKey) []store.TabIndexKey {
	out := make([]store.TabIndexKey, len(keys))
	for i, k := range keys {
		// The discarded ok is the point: the zero UserID IS the "unbindable"
		// marker store.FilterTabIndexKeys drops, so there is nothing to refuse.
		owner, _ := userid.New(k.UserID)
		out[i] = store.TabIndexKey{UserID: owner, TabID: k.TabID}
	}
	return out
}
