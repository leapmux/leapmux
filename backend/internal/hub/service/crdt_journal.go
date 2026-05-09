package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

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

func (j *crdtJournal) LoadState(ctx context.Context, orgID string) (*leapmuxv1.OrgCrdtState, []*leapmuxv1.OpBatch, error) {
	row, err := j.store.OrgState().Get(ctx, orgID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("get org_state: %w", err)
	}
	var state *leapmuxv1.OrgCrdtState
	if err == nil && row != nil {
		state = &leapmuxv1.OrgCrdtState{}
		if uerr := proto.Unmarshal(row.StatePayload, state); uerr != nil {
			return nil, nil, fmt.Errorf("unmarshal state_payload: %w", uerr)
		}
	}
	var watermark *leapmuxv1.HLC
	if state != nil {
		watermark = state.GetCompactionWatermark()
	}
	tail, err := j.listBatchesAfter(ctx, orgID, watermark)
	if err != nil {
		return nil, nil, err
	}
	return state, tail, nil
}

func (j *crdtJournal) listBatchesAfter(ctx context.Context, orgID string, watermark *leapmuxv1.HLC) ([]*leapmuxv1.OpBatch, error) {
	// Page through the journal so a far-behind subscriber cannot load
	// the entire backlog in one slab. The cursor walks forward by the
	// last consumed batch's HLC; we stop once a page comes back
	// short of the limit.
	out := []*leapmuxv1.OpBatch{}
	cur := watermark
	for {
		rows, err := j.store.OrgOpBatches().ListAfter(ctx, store.ListOrgOpBatchesAfterParams{
			OrgID:             orgID,
			AfterPhysicalMs:   cur.GetPhysical(),
			AfterLogical:      cur.GetLogical(),
			AfterOriginClient: cur.GetClientId(),
			Limit:             store.CRDTBatchPageLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("list org_op_batches after watermark: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			batch := &leapmuxv1.OpBatch{}
			if uerr := proto.Unmarshal(r.BatchPayload, batch); uerr != nil {
				return nil, fmt.Errorf("unmarshal org_op_batch %s: %w", r.BatchID, uerr)
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
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		payload, err := proto.Marshal(c.Batch)
		if err != nil {
			return fmt.Errorf("marshal batch %s: %w", c.Batch.GetBatchId(), err)
		}
		ops := c.Batch.GetOps()
		opCount := int64(len(ops))
		first := ops[0].GetCanonicalHlc()
		last := ops[opCount-1].GetCanonicalHlc()
		if err := tx.OrgOpBatches().Insert(ctx, store.InsertOrgOpBatchParams{
			OrgID:        c.OrgID,
			PhysicalMs:   first.GetPhysical(),
			Logical:      first.GetLogical(),
			LastLogical:  last.GetLogical(),
			OriginClient: first.GetClientId(),
			PrincipalID:  c.PrincipalID,
			BatchID:      c.Batch.GetBatchId(),
			BodyHash:     c.DedupRow.BodyHash,
			BatchPayload: payload,
			OpCount:      opCount,
			Epoch:        c.Epoch,
		}); err != nil {
			return fmt.Errorf("insert org_op_batch %s: %w", c.Batch.GetBatchId(), err)
		}
		dr := c.DedupRow
		dCanon := dr.CanonicalFirstHLC
		if err := tx.OrgRecentBatchIDs().Insert(ctx, store.InsertOrgRecentBatchIDParams{
			OrgID:               dr.OrgID,
			BatchID:             dr.BatchID,
			BodyHash:            dr.BodyHash,
			PrincipalID:         dr.PrincipalID,
			CanonicalPhysicalMs: dCanon.GetPhysical(),
			CanonicalLogical:    dCanon.GetLogical(),
			CanonicalClient:     dCanon.GetClientId(),
			OpCount:             dr.OpCount,
			Epoch:               dr.Epoch,
			ExpiresAt:           dr.ExpiresAt,
		}); err != nil {
			return fmt.Errorf("insert dedup row %s: %w", dr.BatchID, err)
		}
		idx := txTabIndexWriter{tx: tx}
		return crdt.ApplyDiff(ctx, idx, c.IndexDiff)
	})
}

func (j *crdtJournal) LookupRecentBatchID(ctx context.Context, orgID, batchID string) (*crdt.RecentBatchRecord, error) {
	row, err := j.store.OrgRecentBatchIDs().Get(ctx, orgID, batchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, crdt.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &crdt.RecentBatchRecord{
		OrgID:             row.OrgID,
		BatchID:           row.BatchID,
		BodyHash:          row.BodyHash,
		PrincipalID:       row.PrincipalID,
		CanonicalFirstHLC: &leapmuxv1.HLC{Physical: row.CanonicalPhysicalMs, Logical: row.CanonicalLogical, ClientId: row.CanonicalClient},
		OpCount:           row.OpCount,
		Epoch:             row.Epoch,
		ExpiresAt:         row.ExpiresAt,
	}, nil
}

func (j *crdtJournal) AdvanceEpoch(ctx context.Context, orgID string, epoch int64, startedAt time.Time) error {
	return j.store.OrgState().AdvanceEpoch(ctx, store.AdvanceOrgEpochParams{
		OrgID:          orgID,
		Epoch:          epoch,
		EpochStartedAt: startedAt,
		UpdatedAt:      startedAt,
	})
}

func (j *crdtJournal) CompactBatch(ctx context.Context, c crdt.CompactBatch) error {
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		payload, err := proto.Marshal(c.State)
		if err != nil {
			return fmt.Errorf("marshal state: %w", err)
		}
		now := time.Now()
		if err := tx.OrgState().Upsert(ctx, store.UpsertOrgStateParams{
			OrgID:          c.State.GetOrgId(),
			StatePayload:   payload,
			CurrentEpoch:   c.State.GetCurrentEpoch(),
			EpochStartedAt: c.State.GetEpochStartedAt().AsTime(),
			UpdatedAt:      now,
		}); err != nil {
			return fmt.Errorf("upsert org_state: %w", err)
		}
		if c.DropThrough != nil {
			if err := tx.OrgOpBatches().DeleteThrough(ctx, store.DeleteOrgOpBatchesThroughParams{
				OrgID:               c.State.GetOrgId(),
				ThroughPhysicalMs:   c.DropThrough.GetPhysical(),
				ThroughLogical:      c.DropThrough.GetLogical(),
				ThroughOriginClient: c.DropThrough.GetClientId(),
			}); err != nil {
				return fmt.Errorf("delete org_op_batches through: %w", err)
			}
		}
		return nil
	})
}

func (j *crdtJournal) CleanupExpiredRecentBatchIDs(ctx context.Context, before time.Time) (int64, error) {
	return j.store.OrgRecentBatchIDs().DeleteExpired(ctx, before)
}

// txTabIndexWriter is a thin adapter from crdt.TabIndexWriter to the
// transactional store.WorkspaceTabIndexStore. All four methods are
// bulk: crdt.ApplyDiff hands the writer the full per-batch slices in
// one call, and the underlying store chunks internally when the
// backend's parameter limit demands it.
type txTabIndexWriter struct {
	tx store.Store
}

func (w txTabIndexWriter) BulkUpsertOwned(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	params := make([]store.UpsertOwnedTabParams, len(rows))
	for i, row := range rows {
		params[i] = store.UpsertOwnedTabParams{
			OrgID:       row.OrgID,
			WorkspaceID: row.WorkspaceID,
			TabType:     row.TabType,
			TabID:       row.TabID,
			WorkerID:    row.WorkerID,
			TileID:      row.TileID,
			Position:    row.Position,
		}
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertOwned(ctx, params)
}

func (w txTabIndexWriter) BulkDeleteOwned(ctx context.Context, keys []crdt.TabKey) error {
	if len(keys) == 0 {
		return nil
	}
	storeKeys := make([]store.TabIndexKey, len(keys))
	for i, k := range keys {
		storeKeys[i] = store.TabIndexKey{OrgID: k.OrgID, TabID: k.TabID}
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteOwned(ctx, storeKeys)
}

func (w txTabIndexWriter) BulkUpsertRendered(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	params := make([]store.UpsertRenderedTabParams, len(rows))
	for i, row := range rows {
		params[i] = store.UpsertRenderedTabParams{
			OrgID:       row.OrgID,
			WorkspaceID: row.WorkspaceID,
			TabType:     row.TabType,
			TabID:       row.TabID,
			WorkerID:    row.WorkerID,
			TileID:      row.TileID,
			Position:    row.Position,
		}
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertRendered(ctx, params)
}

func (w txTabIndexWriter) BulkDeleteRendered(ctx context.Context, keys []crdt.TabKey) error {
	if len(keys) == 0 {
		return nil
	}
	storeKeys := make([]store.TabIndexKey, len(keys))
	for i, k := range keys {
		storeKeys[i] = store.TabIndexKey{OrgID: k.OrgID, TabID: k.TabID}
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteRendered(ctx, storeKeys)
}
