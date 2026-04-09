package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type workspaceTabStore struct {
	conn *pgConn
}

var _ store.WorkspaceTabStore = (*workspaceTabStore)(nil)

func fromDBWorkspaceTab(t gendb.WorkspaceTab) store.WorkspaceTab {
	return store.WorkspaceTab{
		WorkspaceID: t.WorkspaceID,
		WorkerID:    t.WorkerID,
		TabType:     t.TabType,
		TabID:       t.TabID,
		Position:    t.Position,
		TileID:      t.TileID,
	}
}

func (s *workspaceTabStore) Upsert(ctx context.Context, p store.UpsertWorkspaceTabParams) error {
	return mapErr(s.conn.q.UpsertWorkspaceTab(ctx, gendb.UpsertWorkspaceTabParams{
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TabType:     p.TabType,
		TabID:       p.TabID,
		Position:    p.Position,
		TileID:      p.TileID,
	}))
}

func (s *workspaceTabStore) BulkUpsert(ctx context.Context, params []store.UpsertWorkspaceTabParams) error {
	if len(params) == 0 {
		return nil
	}
	if len(params) == 1 {
		return s.Upsert(ctx, params[0])
	}

	query, args := sqlutil.BuildWorkspaceTabBulkUpsertQuery(
		params,
		func(sb *strings.Builder, i int) {
			base := i*6 + 1
			fmt.Fprintf(sb, "($%d, $%d, $%d, $%d, $%d, $%d)", base, base+1, base+2, base+3, base+4, base+5)
		},
		` ON CONFLICT (workspace_id, tab_type, tab_id) DO UPDATE SET worker_id = EXCLUDED.worker_id, position = EXCLUDED.position, tile_id = EXCLUDED.tile_id`,
	)
	_, err := s.conn.exec.Exec(ctx, query, args...)
	return mapErr(err)
}

func (s *workspaceTabStore) Delete(ctx context.Context, p store.DeleteWorkspaceTabParams) error {
	return mapErr(s.conn.q.DeleteWorkspaceTab(ctx, gendb.DeleteWorkspaceTabParams{
		WorkspaceID: p.WorkspaceID,
		TabType:     p.TabType,
		TabID:       p.TabID,
	}))
}

func (s *workspaceTabStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return mapErr(s.conn.q.DeleteWorkspaceTabsByWorker(ctx, workerID))
}

func (s *workspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	return mapErr(s.conn.q.DeleteWorkspaceTabsByWorkspace(ctx, workspaceID))
}

func (s *workspaceTabStore) DeleteWorkerTabsForWorkspace(ctx context.Context, p store.DeleteWorkerTabsForWorkspaceParams) error {
	return mapErr(s.conn.q.DeleteWorkerTabsForWorkspace(ctx, gendb.DeleteWorkerTabsForWorkspaceParams{
		WorkerID:    p.WorkerID,
		WorkspaceID: p.WorkspaceID,
	}))
}

func (s *workspaceTabStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTab, error) {
	rows, err := s.conn.q.ListWorkspaceTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBWorkspaceTab), nil
}

func (s *workspaceTabStore) ListByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTab, error) {
	rows, err := s.conn.q.ListWorkspaceTabsByWorker(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBWorkspaceTab), nil
}

func (s *workspaceTabStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	ids, err := s.conn.q.ListDistinctWorkersByWorkspace(ctx, workspaceID)
	return ids, mapErr(err)
}

func (s *workspaceTabStore) GetMaxPosition(ctx context.Context, workspaceID string) (string, error) {
	pos, err := s.conn.q.GetMaxTabPosition(ctx, workspaceID)
	return pos, mapErr(err)
}
