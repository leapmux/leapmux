package postgres

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
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
// (user_id, tab_id) pair delete.
const bulkDeleteChunkRows = 8192

// upsertParamArrays is the column-major projection of a chunk of
// UpsertOwnedTabParams that pgx forwards as separate text/integer
// arrays to the UNNEST query. The arrays are re-used across chunks
// (see makeUpsertParamArrays + fill) so the per-chunk allocation cost
// stays at zero after the first chunk.
type upsertParamArrays struct {
	userIDs      []string
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
		userIDs:      make([]string, 0, capacity),
		workspaceIDs: make([]string, 0, capacity),
		tabTypes:     make([]int32, 0, capacity),
		tabIDs:       make([]string, 0, capacity),
		workerIDs:    make([]string, 0, capacity),
		tileIDs:      make([]string, 0, capacity),
		positions:    make([]string, 0, capacity),
	}
}

func (p *upsertParamArrays) fill(rows []store.UpsertOwnedTabParams) {
	p.userIDs = p.userIDs[:0]
	p.workspaceIDs = p.workspaceIDs[:0]
	p.tabTypes = p.tabTypes[:0]
	p.tabIDs = p.tabIDs[:0]
	p.workerIDs = p.workerIDs[:0]
	p.tileIDs = p.tileIDs[:0]
	p.positions = p.positions[:0]
	for _, r := range rows {
		p.userIDs = append(p.userIDs, r.UserID.String())
		p.workspaceIDs = append(p.workspaceIDs, r.WorkspaceID)
		p.tabTypes = append(p.tabTypes, int32(r.TabType))
		p.tabIDs = append(p.tabIDs, r.TabID)
		p.workerIDs = append(p.workerIDs, r.WorkerID)
		p.tileIDs = append(p.tileIDs, r.TileID)
		p.positions = append(p.positions, r.Position)
	}
}

// keyArrays mirrors upsertParamArrays for the two-column BulkDelete*
// path: a single allocation per call, reused across chunks. It fills from
// store.BoundTabIndexKey rather than store.TabIndexKey, so the only keys that
// can reach a bind are the ones store.FilterTabIndexKeys already cleared.
type keyArrays struct {
	userIDs []string
	tabIDs  []string
}

func makeKeyArrays(capacity int) keyArrays {
	return keyArrays{
		userIDs: make([]string, 0, capacity),
		tabIDs:  make([]string, 0, capacity),
	}
}

func (k *keyArrays) fill(keys []store.BoundTabIndexKey) {
	k.userIDs = k.userIDs[:0]
	k.tabIDs = k.tabIDs[:0]
	for _, key := range keys {
		k.userIDs = append(k.userIDs, key.Owner())
		k.tabIDs = append(k.tabIDs, key.TabID())
	}
}

// bulkUpsertTabs is the shared body of BulkUpsertOwned and BulkUpsertRendered:
// allocate the column arrays once, walk the input in chunks, and hand each
// filled chunk to exec. The only thing that differs between the two callers is
// which generated query exec runs, so that is all they spell.
//
// It cannot delegate to sqlutil.BulkUpsertTabs: that composes "?"-placeholder
// SQL at runtime, while postgres binds sqlc-generated unnest(...::TEXT[])
// array queries. The chunk arithmetic they DO share lives in
// sqlutil.ChunkRange.
func bulkUpsertTabs(rows []store.UpsertOwnedTabParams, exec func(p *upsertParamArrays) error) error {
	if len(rows) == 0 {
		return nil
	}
	p := makeUpsertParamArrays(min(len(rows), bulkUpsertChunkRows))
	return sqlutil.ChunkRange(len(rows), bulkUpsertChunkRows, func(start, end int) error {
		p.fill(rows[start:end])
		return exec(&p)
	})
}

// bulkDeleteTabs is the shared body of BulkDeleteOwned and BulkDeleteRendered.
//
// It takes keys that are ALREADY filtered, and does not call
// store.FilterTabIndexKeys itself. That is deliberate, not an oversight: the
// store-bind rule in internal/audit requires the FuncDecl that runs a
// classified ownership query (BulkDeleteOwnedTabs / BulkDeleteRenderedTabs) to
// lexically contain a call to a shared owner guard. Pulling the filter in here
// would move the guard out of BulkDeleteOwned / BulkDeleteRendered, and the
// audit would correctly report both as binding an owner column unguarded. Do
// not "clean this up" by hoisting the filter.
func bulkDeleteTabs(bound []store.BoundTabIndexKey, exec func(k *keyArrays) error) error {
	if len(bound) == 0 {
		return nil
	}
	k := makeKeyArrays(min(len(bound), bulkDeleteChunkRows))
	return sqlutil.ChunkRange(len(bound), bulkDeleteChunkRows, func(start, end int) error {
		k.fill(bound[start:end])
		return exec(&k)
	})
}

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertOwnedTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertOwned(ctx context.Context, rows []store.UpsertOwnedTabParams) error {
	return bulkUpsertTabs(rows, func(p *upsertParamArrays) error {
		return mapErr(s.conn.q.BulkUpsertOwnedTabs(ctx, gendb.BulkUpsertOwnedTabsParams{
			UserIds:      p.userIDs,
			WorkspaceIds: p.workspaceIDs,
			TabTypes:     p.tabTypes,
			TabIds:       p.tabIDs,
			WorkerIds:    p.workerIDs,
			TileIds:      p.tileIDs,
			Positions:    p.positions,
		}))
	})
}

func (s *workspaceTabIndexStore) BulkDeleteOwned(ctx context.Context, keys []store.TabIndexKey) error {
	// Skip keys with an unusable owner; one bad key must not cancel its
	// neighbours' deletes. See store.FilterTabIndexKeys. The guard stays in
	// THIS function rather than moving into bulkDeleteTabs -- see the note
	// there. A drop is logged, not swallowed: it means an upstream tenancy
	// invariant broke.
	bound := store.FilterTabIndexKeysForTable("workspace_tab_owned", keys)
	return bulkDeleteTabs(bound, func(k *keyArrays) error {
		return mapErr(s.conn.q.BulkDeleteOwnedTabs(ctx, gendb.BulkDeleteOwnedTabsParams{
			UserIds: k.userIDs,
			TabIds:  k.tabIDs,
		}))
	})
}

func (s *workspaceTabIndexStore) ListOwnedByWorker(ctx context.Context, p store.ListOwnedTabsByWorkerParams) ([]store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListOwnedTabsByWorker(ctx, gendb.ListOwnedTabsByWorkerParams{
		UserID:   owner,
		WorkerID: p.WorkerID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, ownedTabRowFromDB), nil
}

func (s *workspaceTabIndexStore) ListDistinctWorkersByWorkspace(ctx context.Context, p store.ListDistinctWorkersByWorkspaceParams) ([]string, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above.
		return nil, nil
	}
	rows, err := s.conn.q.ListDistinctWorkersByWorkspace(ctx, gendb.ListDistinctWorkersByWorkspaceParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

func (s *workspaceTabIndexStore) GetOwned(ctx context.Context, p store.GetOwnedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above; an ownership gate must refuse rather
		// than bind a blank owner.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetOwnedTab(ctx, gendb.GetOwnedTabParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := ownedTabRowFromDB(row)
	return &out, nil
}

func (s *workspaceTabIndexStore) UpsertRendered(ctx context.Context, p store.UpsertRenderedTabParams) error {
	return mapErr(s.conn.q.UpsertRenderedTab(ctx, gendb.UpsertRenderedTabParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertRendered(ctx context.Context, rows []store.UpsertRenderedTabParams) error {
	return bulkUpsertTabs(rows, func(p *upsertParamArrays) error {
		return mapErr(s.conn.q.BulkUpsertRenderedTabs(ctx, gendb.BulkUpsertRenderedTabsParams{
			UserIds:      p.userIDs,
			WorkspaceIds: p.workspaceIDs,
			TabTypes:     p.tabTypes,
			TabIds:       p.tabIDs,
			WorkerIds:    p.workerIDs,
			TileIds:      p.tileIDs,
			Positions:    p.positions,
		}))
	})
}

func (s *workspaceTabIndexStore) BulkDeleteRendered(ctx context.Context, keys []store.TabIndexKey) error {
	// Skip keys with an unusable owner; one bad key must not cancel its
	// neighbours' deletes. See store.FilterTabIndexKeys. The guard stays in
	// THIS function rather than moving into bulkDeleteTabs -- see the note
	// there. A drop is logged, not swallowed: it means an upstream tenancy
	// invariant broke.
	bound := store.FilterTabIndexKeysForTable("workspace_tab_rendered", keys)
	return bulkDeleteTabs(bound, func(k *keyArrays) error {
		return mapErr(s.conn.q.BulkDeleteRenderedTabs(ctx, gendb.BulkDeleteRenderedTabsParams{
			UserIds: k.userIDs,
			TabIds:  k.tabIDs,
		}))
	})
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspaceIDs(ctx context.Context, p store.ListRenderedTabsByWorkspaceIDsParams) ([]store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	if len(p.WorkspaceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListRenderedTabsByWorkspaceIDs(ctx, gendb.ListRenderedTabsByWorkspaceIDsParams{
		UserID:       owner,
		WorkspaceIds: p.WorkspaceIDs,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, renderedTabRowFromDB), nil
}

func (s *workspaceTabIndexStore) GetRendered(ctx context.Context, p store.GetRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetRenderedTab(ctx, gendb.GetRenderedTabParams{
		UserID:      owner,
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
	out := renderedTabRowFromDB(row)
	return &out, nil
}

func (s *workspaceTabIndexStore) LocateAccessibleRendered(ctx context.Context, p store.LocateAccessibleRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
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
	out := renderedTabRowFromDB(row)
	return &out, nil
}

// ownedTabRowFromDB converts one generated workspace_tab_owned row to the store
// shape. renderedTabRowFromDB below is its byte-for-byte twin over
// gendb.WorkspaceTabRendered, and the pair CANNOT collapse into one generic
// helper: the two tables share a column set but sqlc generates a distinct
// struct per table, and Go permits neither field access on a union type
// parameter nor a []A -> []B slice conversion.
func ownedTabRowFromDB(r gendb.WorkspaceTabOwned) store.WorkspaceTabRow {
	return store.WorkspaceTabRow{
		UserID:      r.UserID,
		WorkspaceID: r.WorkspaceID,
		TabType:     leapmuxv1.TabType(r.TabType),
		TabID:       r.TabID,
		WorkerID:    r.WorkerID,
		TileID:      r.TileID,
		Position:    r.Position,
	}
}

// renderedTabRowFromDB converts one generated workspace_tab_rendered row to the
// store shape. See ownedTabRowFromDB for why the two are not one helper.
func renderedTabRowFromDB(r gendb.WorkspaceTabRendered) store.WorkspaceTabRow {
	return store.WorkspaceTabRow{
		UserID:      r.UserID,
		WorkspaceID: r.WorkspaceID,
		TabType:     leapmuxv1.TabType(r.TabType),
		TabID:       r.TabID,
		WorkerID:    r.WorkerID,
		TileID:      r.TileID,
		Position:    r.Position,
	}
}
