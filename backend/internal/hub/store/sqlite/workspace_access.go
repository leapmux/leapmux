package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type workspaceAccessStore struct {
	conn *sqliteConn
}

var _ store.WorkspaceAccessStore = (*workspaceAccessStore)(nil)

func (s *workspaceAccessStore) Grant(ctx context.Context, p store.GrantWorkspaceAccessParams) error {
	return mapErr(s.conn.q.GrantWorkspaceAccess(ctx, gendb.GrantWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	}))
}

// bulkGrantChunkRows caps the per-statement row count for the hand-
// rolled bulk INSERT below. Two placeholders per row, so 499 rows
// stays safely under sqlite's conservative 999-param cap.
const bulkGrantChunkRows = 499

func (s *workspaceAccessStore) BulkGrant(ctx context.Context, params []store.GrantWorkspaceAccessParams) error {
	return sqlutil.BulkGrantWorkspaceAccess(ctx, s.conn.exec, params, sqlutil.BulkGrantWorkspaceAccessConfig{
		ConflictSuffix: " ON CONFLICT DO NOTHING",
		ChunkRows:      bulkGrantChunkRows,
	}, mapErr)
}

func (s *workspaceAccessStore) Revoke(ctx context.Context, p store.RevokeWorkspaceAccessParams) error {
	return mapErr(s.conn.q.RevokeWorkspaceAccess(ctx, gendb.RevokeWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	}))
}

func (s *workspaceAccessStore) ListByWorkspaceID(ctx context.Context, workspaceID string) ([]store.WorkspaceAccess, error) {
	rows, err := s.conn.q.ListWorkspaceAccessByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	result := make([]store.WorkspaceAccess, len(rows))
	for i, r := range rows {
		result[i] = store.WorkspaceAccess{
			WorkspaceID: r.WorkspaceID,
			UserID:      r.UserID,
			CreatedAt:   r.CreatedAt,
		}
	}
	return result, nil
}

func (s *workspaceAccessStore) HasAccess(ctx context.Context, p store.HasWorkspaceAccessParams) (bool, error) {
	ok, err := s.conn.q.HasWorkspaceAccess(ctx, gendb.HasWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	})
	return ok, mapErr(err)
}

func (s *workspaceAccessStore) ListForUserIn(ctx context.Context, userID string, workspaceIDs []string) ([]string, error) {
	if len(workspaceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListWorkspaceAccessForUserIn(ctx, gendb.ListWorkspaceAccessForUserInParams{
		UserID:       userID,
		WorkspaceIds: workspaceIDs,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

func (s *workspaceAccessStore) Clear(ctx context.Context, workspaceID string) error {
	return mapErr(s.conn.q.ClearWorkspaceAccess(ctx, workspaceID))
}
