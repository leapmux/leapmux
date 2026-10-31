package sqlite

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// bulkUpsertChunkRows is the per-statement row cap for bulk-upsert SQL
// constructed at runtime. SQLite's default SQLITE_MAX_VARIABLE_NUMBER
// is 999 (older builds) and 32766 (3.32+); the eight-column upsert
// uses 8 placeholders per row, so 124 rows (992 placeholders) stays
// under the conservative 999-param cap.
const bulkUpsertChunkRows = 124

// bulkDeleteChunkRows is the per-statement key cap for bulk-delete
// SQL. Two placeholders per (user_id, tab_id) pair -> 499 keys per
// chunk under the 999-param cap (one slot to spare).
const bulkDeleteChunkRows = 499

type workspaceTabIndexStore struct {
	conn *sqliteConn
}

var _ store.WorkspaceTabIndexStore = (*workspaceTabIndexStore)(nil)

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		TabType:     p.TabType,
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
		Rendered:    p.Rendered,
	}))
}

// BulkUpsert runs each chunk of up to bulkUpsertChunkRows rows as a single
// INSERT ... VALUES (...), (...) ON CONFLICT DO UPDATE statement.
func (s *workspaceTabIndexStore) BulkUpsert(ctx context.Context, rows []store.UpsertTabParams) error {
	return sqlutil.BulkUpsertTabs(ctx, s.conn.exec, rows, sqlutil.BulkUpsertTabsConfig{
		ConflictSuffix: " ON CONFLICT (user_id, tab_id) DO UPDATE SET workspace_id = excluded.workspace_id, tab_type = excluded.tab_type, worker_id = excluded.worker_id, tile_id = excluded.tile_id, position = excluded.position, rendered = excluded.rendered",
		ChunkRows:      bulkUpsertChunkRows,
	}, mapErr)
}

// BulkDelete runs each chunk as a single DELETE ... WHERE (user_id, tab_id)
// IN ((?, ?), ...) statement. SQLite supports row-value IN since 3.15
// (Oct 2016), which is well below our minimum.
func (s *workspaceTabIndexStore) BulkDelete(ctx context.Context, keys []store.TabIndexKey) error {
	return sqlutil.BulkDeleteTabs(ctx, s.conn.exec, keys, bulkDeleteChunkRows, mapErr)
}

func (s *workspaceTabIndexStore) ListOwnedByWorker(ctx context.Context, p store.ListOwnedTabsByWorkerParams) ([]store.WorkerTabStateRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListOwnedTabsByWorker(ctx, gendb.ListOwnedTabsByWorkerParams{
		ArchivedSectionType:  leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
		ArchivedArchiveState: int64(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED),
		ActiveArchiveState:   int64(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE),
		UserID:               owner,
		WorkerID:             p.WorkerID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, ownedTabStateRowFromDB), nil
}

func (s *workspaceTabIndexStore) ListOwnedTabsByWorkspace(ctx context.Context, p store.ListOwnedTabsByWorkspaceParams) ([]store.OwnedTabRef, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above.
		return nil, nil
	}
	rows, err := s.conn.q.ListOwnedTabsByWorkspace(ctx, gendb.ListOwnedTabsByWorkspaceParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, ownedTabRefFromDB), nil
}

func (s *workspaceTabIndexStore) GetOwned(ctx context.Context, p store.GetOwnedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above; an ownership gate must refuse rather
		// than bind a blank owner.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetOwnedTab(ctx, gendb.GetOwnedTabParams{
		UserID: owner,
		TabID:  p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := tabRowFromDB(row)
	return &out, nil
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
	return store.MapSlice(rows, tabRowFromDB), nil
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
		TabType:     p.TabType,
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := tabRowFromDB(row)
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
		TabType: p.TabType,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := tabRowFromDB(row)
	return &out, nil
}

// ownedTabRefFromDB converts one ListOwnedTabsByWorkspace row to the store's
// (worker_id, tab_type, tab_id) triple. Named beside tabRowFromDB and used
// through store.MapSlice for the same reason: the conversion is the one place the
// generated column types meet the store's, so it should exist once per dialect
// rather than being open-coded in the query method.
func ownedTabRefFromDB(r gendb.ListOwnedTabsByWorkspaceRow) store.OwnedTabRef {
	return store.OwnedTabRef{
		WorkerID: r.WorkerID,
		TabType:  r.TabType,
		TabID:    r.TabID,
	}
}

func ownedTabStateRowFromDB(r gendb.ListOwnedTabsByWorkerRow) store.WorkerTabStateRow {
	return store.WorkerTabStateRow{
		UserID:       r.UserID,
		WorkspaceID:  r.WorkspaceID,
		TabType:      r.TabType,
		TabID:        r.TabID,
		WorkerID:     r.WorkerID,
		ArchiveState: leapmuxv1.WorkspaceArchiveState(r.ArchiveState),
	}
}

// tabRowFromDB converts one generated workspace_tab_owned row to the store
// shape.
func tabRowFromDB(r gendb.WorkspaceTabOwned) store.WorkspaceTabRow {
	return store.WorkspaceTabRow{
		UserID:      r.UserID,
		WorkspaceID: r.WorkspaceID,
		TabType:     r.TabType,
		TabID:       r.TabID,
		WorkerID:    r.WorkerID,
		TileID:      r.TileID,
		Position:    r.Position,
		Rendered:    r.Rendered,
	}
}
