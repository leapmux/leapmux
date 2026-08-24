package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type cleanupStore struct {
	conn *sqliteConn
}

var _ store.CleanupStore = (*cleanupStore)(nil)

func (s *cleanupStore) HardDeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredUserSessions(ctx, sqltime.NewSQLiteTime(now)))
}

func (s *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkspacesBefore(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkersBefore(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteExpiredRegistrationKeysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteExpiredRegistrationKeysBefore(ctx, sqltime.NewSQLiteTime(cutoff)))
}

func (s *cleanupStore) ClearStalePendingEmails(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ClearStalePendingEmails(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteUsersBefore(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthStates(ctx, sqltime.NewSQLiteTime(now)))
}

func (s *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredPendingOAuthSignups(ctx, sqltime.NewSQLiteTime(now)))
}

func (s *cleanupStore) DeleteExpiredWebAuthnSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredWebAuthnSessions(ctx, sqltime.NewSQLiteTime(now)))
}

func (s *cleanupStore) DeleteExpiredDeviceAuthorizations(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDeviceAuthorizations(ctx, sqltime.NewSQLiteTime(cutoff)))
}

func (s *cleanupStore) DeleteExpiredCLIAuthorizationCodes(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredCLIAuthorizationCodes(ctx, sqltime.NewSQLiteTime(cutoff)))
}

func (s *cleanupStore) DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedAPITokensBefore(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedDelegationTokensBefore(ctx, sqltime.SQLiteNullTimeOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredDelegationTokensBefore(ctx, sqltime.NewSQLiteTime(cutoff)))
}

func (s *cleanupStore) DeleteExpiredAltchaSalts(ctx context.Context) (int64, error) {
	rows, err := s.conn.q.DeleteExpiredAltchaSalts(ctx, sqltime.NewSQLiteTime(time.Now().UTC()))
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
