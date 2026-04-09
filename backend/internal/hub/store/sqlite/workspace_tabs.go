package sqlite

import (
	"context"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
)

type workspaceTabStore struct {
	q    *gendb.Queries
	dbtx gendb.DBTX
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
	return mapErr(s.q.UpsertWorkspaceTab(ctx, gendb.UpsertWorkspaceTabParams{
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

	var sb strings.Builder
	sb.WriteString(`INSERT INTO workspace_tabs (workspace_id, worker_id, tab_type, tab_id, position, tile_id) VALUES `)
	args := make([]interface{}, 0, len(params)*6)
	for i, p := range params {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?)")
		args = append(args, p.WorkspaceID, p.WorkerID, p.TabType, p.TabID, p.Position, p.TileID)
	}
	sb.WriteString(` ON CONFLICT (workspace_id, tab_type, tab_id) DO UPDATE SET worker_id = excluded.worker_id, position = excluded.position, tile_id = excluded.tile_id`)

	_, err := s.dbtx.ExecContext(ctx, sb.String(), args...)
	return mapErr(err)
}

func (s *workspaceTabStore) Delete(ctx context.Context, p store.DeleteWorkspaceTabParams) error {
	return mapErr(s.q.DeleteWorkspaceTab(ctx, gendb.DeleteWorkspaceTabParams{
		WorkspaceID: p.WorkspaceID,
		TabType:     p.TabType,
		TabID:       p.TabID,
	}))
}

func (s *workspaceTabStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return mapErr(s.q.DeleteWorkspaceTabsByWorker(ctx, workerID))
}

func (s *workspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	return mapErr(s.q.DeleteWorkspaceTabsByWorkspace(ctx, workspaceID))
}

func (s *workspaceTabStore) DeleteWorkerTabsForWorkspace(ctx context.Context, p store.DeleteWorkerTabsForWorkspaceParams) error {
	return mapErr(s.q.DeleteWorkerTabsForWorkspace(ctx, gendb.DeleteWorkerTabsForWorkspaceParams{
		WorkerID:    p.WorkerID,
		WorkspaceID: p.WorkspaceID,
	}))
}

func (s *workspaceTabStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTab, error) {
	rows, err := s.q.ListWorkspaceTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBWorkspaceTab), nil
}

func (s *workspaceTabStore) ListByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTab, error) {
	rows, err := s.q.ListWorkspaceTabsByWorker(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBWorkspaceTab), nil
}

func (s *workspaceTabStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	ids, err := s.q.ListDistinctWorkersByWorkspace(ctx, workspaceID)
	return ids, mapErr(err)
}

func (s *workspaceTabStore) GetMaxPosition(ctx context.Context, workspaceID string) (string, error) {
	pos, err := s.q.GetMaxTabPosition(ctx, workspaceID)
	return pos, mapErr(err)
}
