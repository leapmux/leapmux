package mysql

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type cleanupStore struct {
	conn *mysqlConn
}

var _ store.CleanupStore = (*cleanupStore)(nil)

func (s *cleanupStore) HardDeleteExpiredSessions(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredUserSessions(ctx))
}

func (s *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkspacesBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkersBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteExpiredRegistrationKeysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteExpiredRegistrationKeysBefore(ctx, sqltime.NewMySQLTime(cutoff)))
}

func (s *cleanupStore) ClearStalePendingEmails(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ClearStalePendingEmails(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteUsersBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteOrgsBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthStates(ctx))
}

func (s *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredPendingOAuthSignups(ctx))
}

func (s *cleanupStore) DeleteExpiredDeviceAuthorizations(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDeviceAuthorizations(ctx, sqltime.NewMySQLTime(cutoff)))
}

func (s *cleanupStore) DeleteExpiredCLIAuthorizationCodes(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredCLIAuthorizationCodes(ctx, sqltime.NewMySQLTime(cutoff)))
}

func (s *cleanupStore) DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedAPITokensBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedDelegationTokensBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDelegationTokensBefore(ctx, sqltime.NewMySQLTime(cutoff)))
}

func (s *cleanupStore) CompactPublishedRevocationEvents(
	ctx context.Context,
	p store.CompactRevocationEventsParams,
) (int64, error) {
	return newRevocationEventStore(s.conn).CompactPublished(ctx, p.Cutoff)
}
