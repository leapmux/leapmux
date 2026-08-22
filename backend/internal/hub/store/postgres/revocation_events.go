package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

// revocationEventStore embeds the shared RevocationCore, which promotes
// PublishPending, the Acquire/Renew/Release lease methods, and CompactPublished
// directly onto the store -- so those coordination methods are defined once in
// store.RevocationCore instead of forwarded by hand in each dialect. Only the
// genuinely dialect-specific reads (ListPublishedAfter, MaxPublishedSeq) and the
// row-conversion stay here.
type revocationEventStore struct {
	conn *pgConn
	store.RevocationCore[*pgConn]
}

var _ store.RevocationEventStore = (*revocationEventStore)(nil)

// sqlc cannot resolve the pending alias in this valid UPDATE ... FROM query.
const publishPendingRevocationEventsQuery = `
WITH pending AS MATERIALIZED (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at ASC, id ASC) AS seq_offset
    FROM revocation_events
    WHERE seq IS NULL
    ORDER BY created_at ASC, id ASC
    LIMIT $2
)
UPDATE revocation_events AS event
SET seq = $1 + pending.seq_offset,
    published_at = NOW()
FROM pending
WHERE event.id = pending.id
`

func insertRevocationEvent(
	ctx context.Context,
	conn *pgConn,
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
		RevokedAt:          pgtime.New(revokedAt),
		UserAuthGeneration: userAuthGeneration,
	}))
}

// emitCredentialEvent inserts the durable revocation event for a credential
// mutation. Shared by every RunCredentialMutation call site so the kind ->
// insert mapping lives in one place per backend instead of an inline closure.
func emitCredentialEvent(ctx context.Context, conn *pgConn, event store.CredentialEvent) error {
	return insertRevocationEvent(ctx, conn, event.Kind, event.SubjectID, event.UserID, event.At, event.UserAuthGeneration)
}

// revokedCredentialEvent maps a single-token revoke query result into the
// CredentialEvent that RunCredentialMutation emits. A no-row result -- the
// token was already gone, surfaced by pgx as store.ErrNotFound -- yields a nil
// event so the mutation commits zero rows without publishing a spurious
// revocation. Shared by api_tokens and delegation_tokens, whose Revoke bodies
// are otherwise identical.
func revokedCredentialEvent(
	subjectID, userID string,
	revokedAt pgtime.NullTime,
	kind string,
	err error,
) (*store.CredentialEvent, error) {
	if err != nil {
		mapped := mapErr(err)
		if errors.Is(mapped, store.ErrNotFound) {
			return nil, nil
		}
		return nil, mapped
	}
	at, err := sqlutil.RequireTime(revokedAt.Time, revokedAt.Valid, "revoked_at")
	if err != nil {
		return nil, err
	}
	return &store.CredentialEvent{Kind: kind, SubjectID: subjectID, UserID: userID, At: at}, nil
}

func newRevocationEventStore(conn *pgConn) *revocationEventStore {
	return &revocationEventStore{
		conn: conn,
		RevocationCore: store.NewRevocationCore(conn, store.RevocationCoreOps[*pgConn]{
			InTransaction: conn.withTransaction,
			HasPending: func(ctx context.Context, conn *pgConn) (bool, error) {
				hasPending, err := conn.q.HasPendingRevocationEvents(ctx)
				return hasPending, mapErr(err)
			},
			LockSequence: func(ctx context.Context, conn *pgConn) (int64, error) {
				seq, err := conn.q.LockRevocationEventSequence(ctx)
				return seq, mapErr(err)
			},
			PublishRows: func(ctx context.Context, conn *pgConn, limit int32, lastSeq int64) (int64, error) {
				return rowsAffected(conn.exec.Exec(ctx, publishPendingRevocationEventsQuery, lastSeq, limit))
			},
			SetSequence: func(ctx context.Context, conn *pgConn, sequence int64) error {
				return mapErr(conn.q.SetRevocationEventSequence(ctx, sequence))
			},
			DeleteExpiredLease: func(ctx context.Context, conn *pgConn) error {
				_, err := conn.q.DeleteExpiredHubRuntimeLease(ctx)
				return mapErr(err)
			},
			CompactPublished: func(ctx context.Context, conn *pgConn, cutoff time.Time) (int64, error) {
				deleted, err := conn.q.DeleteCompactablePublishedRevocationEvents(ctx, pgtime.NullOf(cutoff))
				return deleted, mapErr(err)
			},
			InsertLease: func(ctx context.Context, conn *pgConn, lease store.RevocationLease) error {
				return mapErr(conn.q.InsertHubRuntimeLease(ctx, gendb.InsertHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: lease.LeaseMillis,
				}))
			},
			RenewLease: func(ctx context.Context, conn *pgConn, lease store.RevocationLease) (int64, error) {
				n, err := conn.q.RenewHubRuntimeLease(ctx, gendb.RenewHubRuntimeLeaseParams{
					HolderID: lease.HolderID, CursorSeq: lease.CursorSeq, LeaseMillis: lease.LeaseMillis,
				})
				return n, mapErr(err)
			},
			ReleaseLease: func(ctx context.Context, conn *pgConn, holderID string) (int64, error) {
				n, err := conn.q.DeleteHubRuntimeLease(ctx, holderID)
				return n, mapErr(err)
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
		Seq:   pgtype.Int8{Int64: afterSeq, Valid: true},
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
		revokedAt := row.RevokedAt.Time
		createdAt := row.CreatedAt.Time
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
				RevokedAt:          revokedAt,
				UserAuthGeneration: row.UserAuthGeneration,
				CreatedAt:          createdAt,
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
