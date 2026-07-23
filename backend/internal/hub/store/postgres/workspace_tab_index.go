package postgres

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type workspaceTabIndexStore struct {
	conn *pgConn
}

var _ store.WorkspaceTabIndexStore = (*workspaceTabIndexStore)(nil)

// bulkUpsertChunkRows is the per-statement row cap for the
// UNNEST-based bulk upsert. Postgres binds each array slot as one
// parameter (so this is effectively bounded by pgx's protocol limit
// of 65535 bound parameters per execute), but we pick a moderate
// chunk to bound peak memory and statement plan time.
const bulkUpsertChunkRows = 4096

// bulkDeleteChunkRows mirrors bulkUpsertChunkRows for the
// (org_id, tab_id) pair delete.
const bulkDeleteChunkRows = 8192

// upsertParamArrays is the column-major projection of a chunk of
// UpsertOwnedTabParams that pgx forwards as separate text/integer
// arrays to the UNNEST query. The arrays are re-used across chunks
// (see makeUpsertParamArrays + fill) so the per-chunk allocation cost
// stays at zero after the first chunk.
type upsertParamArrays struct {
	orgIDs       []string
	workspaceIDs []string
	tabTypes     []int32
	tabIDs       []string
	workerIDs    []string
	tileIDs      []string
	positions    []string
}

// makeUpsertParamArrays allocates the column arrays once, sized for
// the worst-case chunk. Subsequent chunks call fill which truncates
// the existing backing arrays without reallocating.
func makeUpsertParamArrays(capacity int) upsertParamArrays {
	return upsertParamArrays{
		orgIDs:       make([]string, 0, capacity),
		workspaceIDs: make([]string, 0, capacity),
		tabTypes:     make([]int32, 0, capacity),
		tabIDs:       make([]string, 0, capacity),
		workerIDs:    make([]string, 0, capacity),
		tileIDs:      make([]string, 0, capacity),
		positions:    make([]string, 0, capacity),
	}
}

func (p *upsertParamArrays) fill(rows []store.UpsertOwnedTabParams) {
	p.orgIDs = p.orgIDs[:0]
	p.workspaceIDs = p.workspaceIDs[:0]
	p.tabTypes = p.tabTypes[:0]
	p.tabIDs = p.tabIDs[:0]
	p.workerIDs = p.workerIDs[:0]
	p.tileIDs = p.tileIDs[:0]
	p.positions = p.positions[:0]
	for _, r := range rows {
		p.orgIDs = append(p.orgIDs, r.OrgID)
		p.workspaceIDs = append(p.workspaceIDs, r.WorkspaceID)
		p.tabTypes = append(p.tabTypes, int32(r.TabType))
		p.tabIDs = append(p.tabIDs, r.TabID)
		p.workerIDs = append(p.workerIDs, r.WorkerID)
		p.tileIDs = append(p.tileIDs, r.TileID)
		p.positions = append(p.positions, r.Position)
	}
}

// keyArrays mirrors upsertParamArrays for the two-column BulkDelete*
// path: a single allocation per call, reused across chunks.
type keyArrays struct {
	orgIDs []string
	tabIDs []string
}

func makeKeyArrays(capacity int) keyArrays {
	return keyArrays{
		orgIDs: make([]string, 0, capacity),
		tabIDs: make([]string, 0, capacity),
	}
}

func (k *keyArrays) fill(keys []store.TabIndexKey) {
	k.orgIDs = k.orgIDs[:0]
	k.tabIDs = k.tabIDs[:0]
	for _, key := range keys {
		k.orgIDs = append(k.orgIDs, key.OrgID)
		k.tabIDs = append(k.tabIDs, key.TabID)
	}
}

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertOwnedTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		OrgID:       p.OrgID,
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertOwned(ctx context.Context, rows []store.UpsertOwnedTabParams) error {
	if len(rows) == 0 {
		return nil
	}
	cap := bulkUpsertChunkRows
	if len(rows) < cap {
		cap = len(rows)
	}
	p := makeUpsertParamArrays(cap)
	for start := 0; start < len(rows); start += bulkUpsertChunkRows {
		end := start + bulkUpsertChunkRows
		if end > len(rows) {
			end = len(rows)
		}
		p.fill(rows[start:end])
		if err := s.conn.q.BulkUpsertOwnedTabs(ctx, gendb.BulkUpsertOwnedTabsParams{
			OrgIds:       p.orgIDs,
			WorkspaceIds: p.workspaceIDs,
			TabTypes:     p.tabTypes,
			TabIds:       p.tabIDs,
			WorkerIds:    p.workerIDs,
			TileIds:      p.tileIDs,
			Positions:    p.positions,
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *workspaceTabIndexStore) DeleteOwned(ctx context.Context, orgID, tabID string) error {
	return mapErr(s.conn.q.DeleteOwnedTab(ctx, gendb.DeleteOwnedTabParams{OrgID: orgID, TabID: tabID}))
}

func (s *workspaceTabIndexStore) BulkDeleteOwned(ctx context.Context, keys []store.TabIndexKey) error {
	if len(keys) == 0 {
		return nil
	}
	cap := bulkDeleteChunkRows
	if len(keys) < cap {
		cap = len(keys)
	}
	k := makeKeyArrays(cap)
	for start := 0; start < len(keys); start += bulkDeleteChunkRows {
		end := start + bulkDeleteChunkRows
		if end > len(keys) {
			end = len(keys)
		}
		k.fill(keys[start:end])
		if err := s.conn.q.BulkDeleteOwnedTabs(ctx, gendb.BulkDeleteOwnedTabsParams{
			OrgIds: k.orgIDs,
			TabIds: k.tabIDs,
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *workspaceTabIndexStore) DeleteOwnedByOrg(ctx context.Context, orgID string) error {
	return mapErr(s.conn.q.DeleteOwnedTabsByOrg(ctx, orgID))
}

func (s *workspaceTabIndexStore) ListOwnedByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListOwnedTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out, nil
}

func (s *workspaceTabIndexStore) ListOwnedByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListOwnedTabsByWorker(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out, nil
}

func (s *workspaceTabIndexStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := s.conn.q.ListDistinctWorkersByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

func (s *workspaceTabIndexStore) GetOwned(ctx context.Context, p store.GetOwnedTabParams) (*store.WorkspaceTabRow, error) {
	row, err := s.conn.q.GetOwnedTab(ctx, gendb.GetOwnedTabParams{
		WorkspaceID: p.WorkspaceID,
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}

func (s *workspaceTabIndexStore) UpsertRendered(ctx context.Context, p store.UpsertRenderedTabParams) error {
	return mapErr(s.conn.q.UpsertRenderedTab(ctx, gendb.UpsertRenderedTabParams{
		OrgID:       p.OrgID,
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertRendered(ctx context.Context, rows []store.UpsertRenderedTabParams) error {
	if len(rows) == 0 {
		return nil
	}
	cap := bulkUpsertChunkRows
	if len(rows) < cap {
		cap = len(rows)
	}
	p := makeUpsertParamArrays(cap)
	for start := 0; start < len(rows); start += bulkUpsertChunkRows {
		end := start + bulkUpsertChunkRows
		if end > len(rows) {
			end = len(rows)
		}
		p.fill(rows[start:end])
		if err := s.conn.q.BulkUpsertRenderedTabs(ctx, gendb.BulkUpsertRenderedTabsParams{
			OrgIds:       p.orgIDs,
			WorkspaceIds: p.workspaceIDs,
			TabTypes:     p.tabTypes,
			TabIds:       p.tabIDs,
			WorkerIds:    p.workerIDs,
			TileIds:      p.tileIDs,
			Positions:    p.positions,
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *workspaceTabIndexStore) DeleteRendered(ctx context.Context, orgID, tabID string) error {
	return mapErr(s.conn.q.DeleteRenderedTab(ctx, gendb.DeleteRenderedTabParams{OrgID: orgID, TabID: tabID}))
}

func (s *workspaceTabIndexStore) BulkDeleteRendered(ctx context.Context, keys []store.TabIndexKey) error {
	if len(keys) == 0 {
		return nil
	}
	cap := bulkDeleteChunkRows
	if len(keys) < cap {
		cap = len(keys)
	}
	k := makeKeyArrays(cap)
	for start := 0; start < len(keys); start += bulkDeleteChunkRows {
		end := start + bulkDeleteChunkRows
		if end > len(keys) {
			end = len(keys)
		}
		k.fill(keys[start:end])
		if err := s.conn.q.BulkDeleteRenderedTabs(ctx, gendb.BulkDeleteRenderedTabsParams{
			OrgIds: k.orgIDs,
			TabIds: k.tabIDs,
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *workspaceTabIndexStore) DeleteRenderedByOrg(ctx context.Context, orgID string) error {
	return mapErr(s.conn.q.DeleteRenderedTabsByOrg(ctx, orgID))
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListRenderedTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return renderedTabsFromDB(rows), nil
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspaceIDs(ctx context.Context, workspaceIDs []string) ([]store.WorkspaceTabRow, error) {
	if len(workspaceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListRenderedTabsByWorkspaceIDs(ctx, workspaceIDs)
	if err != nil {
		return nil, mapErr(err)
	}
	return renderedTabsFromDB(rows), nil
}

func renderedTabsFromDB(rows []gendb.WorkspaceTabRendered) []store.WorkspaceTabRow {
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out
}

func (s *workspaceTabIndexStore) GetRendered(ctx context.Context, p store.GetRenderedTabParams) (*store.WorkspaceTabRow, error) {
	row, err := s.conn.q.GetRenderedTab(ctx, gendb.GetRenderedTabParams{
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}

func (s *workspaceTabIndexStore) LocateAccessibleRendered(ctx context.Context, p store.LocateAccessibleRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.LocateAccessibleRenderedTab(ctx, gendb.LocateAccessibleRenderedTabParams{
		UserID:  owner,
		TabID:   p.TabID,
		TabType: int32(p.TabType),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}
