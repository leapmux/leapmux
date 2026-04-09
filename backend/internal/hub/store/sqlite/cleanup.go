package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
)

type cleanupStore struct {
	conn *sqliteConn
}

var _ store.CleanupStore = (*cleanupStore)(nil)

func (s *cleanupStore) HardDeleteExpiredSessions(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredUserSessions(ctx))
}

func (s *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkspacesBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}))
}

func (s *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkersBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}))
}

func (s *cleanupStore) HardDeleteExpiredRegistrationsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteExpiredRegistrationsBefore(ctx, cutoff))
}

func (s *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteUsersBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}))
}

func (s *cleanupStore) HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteOrgsBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}))
}

func (s *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthStates(ctx))
}

func (s *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredPendingOAuthSignups(ctx))
}
