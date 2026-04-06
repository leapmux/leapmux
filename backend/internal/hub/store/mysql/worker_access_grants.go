package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// workerAccessGrantStore implements store.WorkerAccessGrantStore backed by MySQL.
type workerAccessGrantStore struct{ q *gendb.Queries }

var _ store.WorkerAccessGrantStore = (*workerAccessGrantStore)(nil)

func (s *workerAccessGrantStore) Grant(ctx context.Context, p store.GrantWorkerAccessParams) error {
	return mapErr(s.q.GrantWorkerAccess(ctx, gendb.GrantWorkerAccessParams{
		WorkerID:  p.WorkerID,
		UserID:    p.UserID,
		GrantedBy: p.GrantedBy,
	}))
}

func (s *workerAccessGrantStore) Revoke(ctx context.Context, p store.RevokeWorkerAccessParams) error {
	return mapErr(s.q.RevokeWorkerAccess(ctx, gendb.RevokeWorkerAccessParams{
		WorkerID: p.WorkerID,
		UserID:   p.UserID,
	}))
}

func (s *workerAccessGrantStore) List(ctx context.Context, workerID string) ([]store.WorkerAccessGrant, error) {
	rows, err := s.q.ListWorkerAccessGrants(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	return sqlutil.MapSlice(rows, fromDBWorkerAccessGrant), nil
}

func (s *workerAccessGrantStore) HasAccess(ctx context.Context, p store.HasWorkerAccessParams) (bool, error) {
	ok, err := s.q.HasWorkerAccess(ctx, gendb.HasWorkerAccessParams{
		WorkerID: p.WorkerID,
		UserID:   p.UserID,
	})
	return ok, mapErr(err)
}

func (s *workerAccessGrantStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return mapErr(s.q.DeleteWorkerAccessGrantsByWorker(ctx, workerID))
}

func (s *workerAccessGrantStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.q.DeleteWorkerAccessGrantsByUser(ctx, userID))
}

func (s *workerAccessGrantStore) DeleteByUserInOrg(ctx context.Context, p store.DeleteWorkerAccessGrantsByUserInOrgParams) error {
	return mapErr(s.q.DeleteWorkerAccessGrantsByUserInOrg(ctx, gendb.DeleteWorkerAccessGrantsByUserInOrgParams{
		UserID: p.UserID,
		OrgID:  p.OrgID,
	}))
}

func fromDBWorkerAccessGrant(g gendb.WorkerAccessGrant) store.WorkerAccessGrant {
	return store.WorkerAccessGrant{
		WorkerID:  g.WorkerID,
		UserID:    g.UserID,
		GrantedBy: g.GrantedBy,
		CreatedAt: g.CreatedAt,
	}
}
