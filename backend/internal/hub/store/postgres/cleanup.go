package postgres

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type cleanupStore struct {
	conn *pgConn
}

var _ store.CleanupStore = (*cleanupStore)(nil)

func (s *cleanupStore) HardDeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredUserSessions(ctx, pgtime.New(now)))
}

func (s *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkspacesBefore(ctx, pgtime.NullOf(cutoff)))
}

func (s *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteWorkersBefore(ctx, pgtime.NullOf(cutoff)))
}

func (s *cleanupStore) HardDeleteExpiredRegistrationKeysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteExpiredRegistrationKeysBefore(ctx, pgtime.New(cutoff)))
}

func (s *cleanupStore) ClearStalePendingEmails(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ClearStalePendingEmails(ctx, gendb.ClearStalePendingEmailsParams{
		Cutoff:         pgtime.NullOf(cutoff),
		CodelessCutoff: pgtime.New(cutoff),
	}))
}

func (s *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.HardDeleteUsersBefore(ctx, pgtime.NullOf(cutoff)))
}

func (s *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredOAuthStates(ctx, pgtime.New(now)))
}

func (s *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredPendingOAuthSignups(ctx, pgtime.New(now)))
}

func (s *cleanupStore) DeleteExpiredWebAuthnSessions(ctx context.Context, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredWebAuthnSessions(ctx, pgtime.New(now)))
}

func (s *cleanupStore) DeleteExpiredDeviceAuthorizations(ctx context.Context, now time.Time) (int64, error) {
	return s.conn.q.DeleteExpiredDeviceAuthorizations(ctx, pgtime.New(now))
}

func (s *cleanupStore) DeleteExpiredOAuthAuthorizationCodes(ctx context.Context, now time.Time) (int64, error) {
	return s.conn.q.DeleteExpiredOAuthAuthorizationCodes(ctx, pgtime.New(now))
}

func (s *cleanupStore) DeleteExpiredAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteExpiredAPITokensBefore(ctx, pgtime.NullOf(cutoff))
}

func (s *cleanupStore) DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteRevokedAPITokensBefore(ctx, pgtime.NullOf(cutoff))
}

func (s *cleanupStore) DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteRevokedDelegationTokensBefore(ctx, pgtime.NullOf(cutoff))
}

func (s *cleanupStore) DeleteExpiredDelegationTokensBefore(ctx context.Context, now time.Time) (int64, error) {
	return s.conn.q.DeleteExpiredDelegationTokensBefore(ctx, pgtime.New(now))
}

func (s *cleanupStore) DeleteExpiredAltchaSalts(ctx context.Context) (int64, error) {
	return s.conn.q.DeleteExpiredAltchaSalts(ctx, pgtime.New(time.Now().UTC()))
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
