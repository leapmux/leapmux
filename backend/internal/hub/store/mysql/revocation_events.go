package mysql

import (
	"context"
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// revocationEventStore embeds the shared RevocationCore, which promotes
// PublishPending, the Acquire/Renew/Release lease methods, and CompactPublished
// directly onto the store -- so those coordination methods are defined once in
// store.RevocationCore instead of forwarded by hand in each dialect. Only the
// genuinely dialect-specific reads (ListPublishedAfter, MaxPublishedSeq) and the
// row-conversion stay here.
type revocationEventStore struct {
	conn *mysqlConn
	store.RevocationCore[*mysqlConn]
}

var _ store.RevocationEventStore = (*revocationEventStore)(nil)

// sqlc cannot resolve the pending alias in this valid multi-table UPDATE.
const publishPendingRevocationEventsQuery = `
UPDATE revocation_events AS event
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at ASC, id ASC) AS seq_offset
    FROM revocation_events
    WHERE seq IS NULL
    ORDER BY created_at ASC, id ASC
    LIMIT ?
) AS pending ON pending.id = event.id
SET event.seq = ? + pending.seq_offset,
    event.published_at = CURRENT_TIMESTAMP(6)
`

func insertRevocationEvent(
	ctx context.Context,
	conn *mysqlConn,
	kind string,
	subjectID string,
	userID string,
	revokedAt time.Time,
	userAuthGeneration int64,
) error {
	return mapErr(conn.q.InsertRevocationEvent(ctx, gendb.InsertRevocationEventParams{
		ID:                 id.Generate(),
		Kind:               kind,
		SubjectID:          subjectID,
		UserID:             userID,
		RevokedAt:          revokedAt.UTC(),
		UserAuthGeneration: userAuthGeneration,
	}))
}

// emitCredentialEvent inserts the durable revocation event for a credential
// mutation. Shared by every RunCredentialMutation call site so the kind ->
// insert mapping lives in one place per backend instead of an inline closure.
func emitCredentialEvent(ctx context.Context, conn *mysqlConn, event store.CredentialEvent) error {
	return insertRevocationEvent(ctx, conn, event.Kind, event.SubjectID, event.UserID, event.At, event.UserAuthGeneration)
}

func newRevocationEventStore(conn *mysqlConn) *revocationEventStore {
	return &revocationEventStore{
		conn: conn,
		RevocationCore: store.NewRevocationCore(conn, store.RevocationCoreOps[*mysqlConn]{
			InTransaction: conn.withTransaction,
			HasPending: func(ctx context.Context, conn *mysqlConn) (bool, error) {
				hasPending, err := conn.q.HasPendingRevocationEvents(ctx)
				return hasPending, mapErr(err)
			},
			LockSequence: func(ctx context.Context, conn *mysqlConn) (int64, error) {
				seq, err := conn.q.LockRevocationEventSequence(ctx)
				return seq, mapErr(err)
			},
			PublishRows: func(ctx context.Context, conn *mysqlConn, limit int32, lastSeq int64) (int64, error) {
				return rowsAffected(conn.exec.ExecContext(ctx, publishPendingRevocationEventsQuery, limit, lastSeq))
			},
			SetSequence: func(ctx context.Context, conn *mysqlConn, sequence int64) error {
				return mapErr(conn.q.SetRevocationEventSequence(ctx, sequence))
			},
			DeleteExpiredLease: func(ctx context.Context, conn *mysqlConn) error {
				_, err := conn.q.DeleteExpiredHubRuntimeLease(ctx)
				return mapErr(err)
			},
			CompactPublished: func(ctx context.Context, conn *mysqlConn, cutoff time.Time) (int64, error) {
				return rowsAffected(conn.q.DeleteCompactablePublishedRevocationEvents(ctx, sql.NullTime{Time: cutoff, Valid: true}))
			},
			InsertLease: func(ctx context.Context, conn *mysqlConn, lease store.RevocationLease) error {
				return mapErr(conn.q.InsertHubRuntimeLease(ctx, gendb.InsertHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: lease.LeaseMillis,
				}))
			},
			RenewLease: func(ctx context.Context, conn *mysqlConn, lease store.RevocationLease) (int64, error) {
				return rowsAffected(conn.q.RenewHubRuntimeLease(ctx, gendb.RenewHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: lease.LeaseMillis,
				}))
			},
			ReleaseLease: func(ctx context.Context, conn *mysqlConn, holderID string) (int64, error) {
				return rowsAffected(conn.q.DeleteHubRuntimeLease(ctx, holderID))
			},
		}),
	}
}

func (s *revocationEventStore) ListPublishedAfter(
	ctx context.Context,
	afterSeq int64,
	limit int32,
) ([]store.PublishedRevocationEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListPublishedRevocationEventsAfter(ctx, gendb.ListPublishedRevocationEventsAfterParams{
		Seq:   sql.NullInt64{Int64: afterSeq, Valid: true},
		Limit: limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.PublishedRevocationEvent, len(rows))
	for i, row := range rows {
		seq, err := sqlutil.RequireInt64(row.Seq.Int64, row.Seq.Valid, "seq")
		if err != nil {
			return nil, err
		}
		publishedAt, err := sqlutil.RequireTime(row.PublishedAt.Time, row.PublishedAt.Valid, "published_at")
		if err != nil {
			return nil, err
		}
		out[i] = store.PublishedRevocationEvent{
			Seq: seq,
			Event: store.RevocationEvent{
				ID:                 row.ID,
				Kind:               row.Kind,
				SubjectID:          row.SubjectID,
				UserID:             row.UserID,
				RevokedAt:          row.RevokedAt.UTC(),
				UserAuthGeneration: row.UserAuthGeneration,
				CreatedAt:          row.CreatedAt.UTC(),
			},
			PublishedAt: publishedAt,
		}
	}
	return out, nil
}

func (s *revocationEventStore) MaxPublishedSeq(ctx context.Context) (int64, error) {
	seq, err := s.conn.q.MaxPublishedRevocationEventSeq(ctx)
	return seq, mapErr(err)
}

func mysqlRevocationNow(ctx context.Context, conn *mysqlConn) (time.Time, error) {
	now, err := conn.q.RevocationNow(ctx)
	if err != nil {
		return time.Time{}, mapErr(err)
	}
	return now.UTC(), nil
}
