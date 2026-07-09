package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
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
	conn *sqliteConn
	store.RevocationCore[*sqliteConn]
}

var _ store.RevocationEventStore = (*revocationEventStore)(nil)

// sqlc's SQLite parser cannot resolve a materialized CTE from UPDATE ... FROM.
// MATERIALIZED is required because a correlated subquery can observe rows that
// this same statement already published and assign duplicate sequence values.
const publishPendingRevocationEventsQuery = `
WITH pending AS MATERIALIZED (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at ASC, id ASC) AS seq_offset
    FROM revocation_events
    WHERE seq IS NULL
    ORDER BY created_at ASC, id ASC
    LIMIT ?
)
UPDATE revocation_events AS event
SET seq = ? + pending.seq_offset,
    published_at = (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
FROM pending
WHERE event.id = pending.id
`

func insertRevocationEvent(
	ctx context.Context,
	conn *sqliteConn,
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
func emitCredentialEvent(ctx context.Context, conn *sqliteConn, event store.CredentialEvent) error {
	return insertRevocationEvent(ctx, conn, event.Kind, event.SubjectID, event.UserID, event.At, event.UserAuthGeneration)
}

// revokedCredentialEvent maps a single-token revoke query result into the
// CredentialEvent that RunCredentialMutation emits. A no-row result -- the
// token was already gone -- yields a nil event so the mutation commits zero
// rows without publishing a spurious revocation. Shared by api_tokens and
// delegation_tokens, whose Revoke bodies are otherwise identical.
func revokedCredentialEvent(
	subjectID, userID string,
	revokedAt sql.NullTime,
	kind string,
	err error,
) (*store.CredentialEvent, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	at, err := sqlutil.RequireTime(revokedAt.Time, revokedAt.Valid, "revoked_at")
	if err != nil {
		return nil, err
	}
	return &store.CredentialEvent{Kind: kind, SubjectID: subjectID, UserID: userID, At: at}, nil
}

func newRevocationEventStore(conn *sqliteConn) *revocationEventStore {
	return &revocationEventStore{
		conn: conn,
		RevocationCore: store.NewRevocationCore(conn, store.RevocationCoreOps[*sqliteConn]{
			InTransaction: conn.withTransaction,
			HasPending: func(ctx context.Context, conn *sqliteConn) (bool, error) {
				hasPending, err := conn.q.HasPendingRevocationEvents(ctx)
				return hasPending, mapErr(err)
			},
			LockSequence: func(ctx context.Context, conn *sqliteConn) (int64, error) {
				seq, err := conn.q.LockRevocationEventSequence(ctx)
				return seq, mapErr(err)
			},
			PublishRows: func(ctx context.Context, conn *sqliteConn, limit int32, lastSeq int64) (int64, error) {
				return rowsAffected(conn.exec.ExecContext(ctx, publishPendingRevocationEventsQuery, limit, lastSeq))
			},
			SetSequence: func(ctx context.Context, conn *sqliteConn, sequence int64) error {
				return mapErr(conn.q.SetRevocationEventSequence(ctx, sequence))
			},
			DeleteExpiredLease: func(ctx context.Context, conn *sqliteConn) error {
				_, err := conn.q.DeleteExpiredHubRuntimeLease(ctx)
				return mapErr(err)
			},
			CompactPublished: func(ctx context.Context, conn *sqliteConn, cutoff time.Time) (int64, error) {
				return rowsAffected(conn.q.DeleteCompactablePublishedRevocationEvents(ctx, cutoff))
			},
			InsertLease: func(ctx context.Context, conn *sqliteConn, lease store.RevocationLease) error {
				return mapErr(conn.q.InsertHubRuntimeLease(ctx, gendb.InsertHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: float64(lease.LeaseMillis),
				}))
			},
			RenewLease: func(ctx context.Context, conn *sqliteConn, lease store.RevocationLease) (int64, error) {
				return rowsAffected(conn.q.RenewHubRuntimeLease(ctx, gendb.RenewHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: float64(lease.LeaseMillis),
				}))
			},
			ReleaseLease: func(ctx context.Context, conn *sqliteConn, holderID string) (int64, error) {
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
		Limit: int64(limit),
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
