package mysql

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type cleanupStore struct {
	conn *mysqlConn
}

var _ store.CleanupStore = (*cleanupStore)(nil)

func (s *cleanupStore) HardDeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredUserSessions(ctx, sqltime.NewMySQLTime(now)))
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
	return rowsAffected(s.conn.q.ClearStalePendingEmails(ctx, gendb.ClearStalePendingEmailsParams{
		Cutoff:         sqltime.MySQLNullTimeOf(cutoff),
		CodelessCutoff: sqltime.NewMySQLTime(cutoff),
	}))
}

func (s *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteUsersBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthStates(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredPendingOAuthSignups(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredWebAuthnSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredWebAuthnSessions(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredDeviceAuthorizations(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDeviceAuthorizations(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredOAuthAuthorizationCodes(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthAuthorizationCodes(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// A params struct rather than a bare argument: the statement compares the
	// cutoff against both deadlines, so sqlc binds the one named parameter
	// twice.
	return rowsAffected(s.conn.q.DeleteExpiredAPITokensBefore(ctx, gendb.DeleteExpiredAPITokensBeforeParams{
		Cutoff: sqltime.MySQLNullTimeOf(cutoff),
	}))
}

func (s *cleanupStore) DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedAPITokensBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedDelegationTokensBefore(ctx, sqltime.MySQLNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredDelegationTokensBefore(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDelegationTokensBefore(ctx, sqltime.NewMySQLTime(now)))
}

func (s *cleanupStore) DeleteExpiredAltchaSalts(ctx context.Context) (int64, error) {
	rows, err := s.conn.q.DeleteExpiredAltchaSalts(ctx, sqltime.NewMySQLTime(time.Now().UTC()))
	if err != nil {
		return 0, mapErr(err)
	}
	return rows, nil
}

func (s *cleanupStore) CompactPublishedRevocationEvents(
	ctx context.Context,
	p store.CompactRevocationEventsParams,
) (int64, error) {
	return newRevocationEventStore(s.conn).CompactPublished(ctx, p.Cutoff)
}

// DeleteUserOpBatchesBeforePhysical is the retention backstop for CRDT
// op batches -- see the query's own doc for why the HLC-based compaction tick
// cannot drain a dormant account, and why the cutoff is an HLC physical rather
// than the committed_at wall clock.
func (s *cleanupStore) DeleteUserOpBatchesBeforePhysical(ctx context.Context, cutoffPhysicalMs int64) (int64, error) {
	return rowsAffected(s.conn.q.DeleteUserOpBatchesBeforePhysical(ctx, cutoffPhysicalMs))
}
