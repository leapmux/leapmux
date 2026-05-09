package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type delegationTokenStore struct{ conn *pgConn }

var _ store.DelegationTokenStore = (*delegationTokenStore)(nil)

func fromDBDelegationToken(t gendb.DelegationToken) store.DelegationToken {
	return store.DelegationToken{
		ID:                       t.ID,
		UserID:                   t.UserID,
		WorkerID:                 t.WorkerID,
		WorkspaceID:              t.WorkspaceID,
		AgentID:                  t.AgentID,
		TerminalID:               t.TerminalID,
		IssuedForTabID:           t.IssuedForTabID,
		IssuedForTabType:         t.IssuedForTabType,
		SecretHash:               t.SecretHash,
		RefreshHash:              t.RefreshHash,
		PreviousRefreshHash:      t.PreviousRefreshHash,
		PreviousRefreshExpiresAt: tsToTimePtr(t.PreviousRefreshExpiresAt),
		CreatedAt:                tsToTime(t.CreatedAt),
		LastUsedAt:               tsToTimePtr(t.LastUsedAt),
		ExpiresAt:                tsToTime(t.ExpiresAt),
		RefreshExpiresAt:         tsToTimePtr(t.RefreshExpiresAt),
		RevokedAt:                tsToTimePtr(t.RevokedAt),
	}
}

func (s *delegationTokenStore) Create(ctx context.Context, p store.CreateDelegationTokenParams) error {
	return mapErr(s.conn.q.CreateDelegationToken(ctx, gendb.CreateDelegationTokenParams{
		ID:               p.ID,
		UserID:           p.UserID,
		WorkerID:         p.WorkerID,
		WorkspaceID:      p.WorkspaceID,
		AgentID:          p.AgentID,
		TerminalID:       p.TerminalID,
		IssuedForTabID:   p.IssuedForTabID,
		IssuedForTabType: p.IssuedForTabType,
		SecretHash:       p.SecretHash,
		RefreshHash:      p.RefreshHash,
		ExpiresAt:        timeToTs(p.ExpiresAt),
		RefreshExpiresAt: timePtrToTs(p.RefreshExpiresAt),
	}))
}

func (s *delegationTokenStore) GetByID(ctx context.Context, id string) (*store.DelegationToken, error) {
	t, err := s.conn.q.GetDelegationTokenByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBDelegationToken(t)
	return &out, nil
}

func (s *delegationTokenStore) ListByUser(ctx context.Context, userID string) ([]store.DelegationToken, error) {
	rows, err := s.conn.q.ListDelegationTokensByUser(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.DelegationToken, len(rows))
	for i, r := range rows {
		out[i] = fromDBDelegationToken(r)
	}
	return out, nil
}

func (s *delegationTokenStore) ListActiveByUser(ctx context.Context, userID string) ([]store.DelegationToken, error) {
	rows, err := s.conn.q.ListActiveDelegationTokensByUser(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.DelegationToken, len(rows))
	for i, r := range rows {
		out[i] = fromDBDelegationToken(r)
	}
	return out, nil
}

func (s *delegationTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchDelegationToken(ctx, id))
}

func (s *delegationTokenStore) RotateRefresh(ctx context.Context, p store.RotateDelegationTokenRefreshParams) error {
	return mapErr(s.conn.q.RotateDelegationTokenRefresh(ctx, gendb.RotateDelegationTokenRefreshParams{
		ID:                   p.ID,
		NewRefreshHash:       p.NewRefreshHash,
		NewRefreshExpiresAt:  timePtrToTs(p.NewRefreshExpiresAt),
		PrevRefreshHash:      p.PreviousRefreshHash,
		PrevRefreshExpiresAt: timePtrToTs(p.PreviousRefreshExpiresAt),
	}))
}

func (s *delegationTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return s.conn.q.RevokeDelegationToken(ctx, id)
}

func (s *delegationTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	return s.conn.q.RevokeDelegationTokensByUser(ctx, userID)
}

func (s *delegationTokenStore) ListRevokedSince(ctx context.Context, since time.Time) ([]store.TokenRevocationRecord, error) {
	rows, err := s.conn.q.ListDelegationTokensRevokedSince(ctx, timeToTs(since))
	if err != nil {
		return nil, mapErr(err)
	}
	return mapRevocations(rows,
		func(r gendb.ListDelegationTokensRevokedSinceRow) string { return r.ID },
		func(r gendb.ListDelegationTokensRevokedSinceRow) string { return r.UserID },
		func(r gendb.ListDelegationTokensRevokedSinceRow) pgtype.Timestamptz { return r.RevokedAt },
	), nil
}

func (s *delegationTokenStore) DeleteRevokedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteRevokedDelegationTokensBefore(ctx, timeToTs(cutoff))
}

func (s *delegationTokenStore) DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteExpiredDelegationTokensBefore(ctx, timeToTs(cutoff))
}

// MaxRevokedAt returns the latest delegation_tokens.revoked_at, or
// the zero time when no rows have been revoked.
func (s *delegationTokenStore) MaxRevokedAt(ctx context.Context) (time.Time, error) {
	ts, err := s.conn.q.MaxDelegationTokenRevokedAt(ctx)
	if err != nil {
		mapped := mapErr(err)
		if errors.Is(mapped, store.ErrNotFound) {
			return time.Time{}, nil
		}
		return time.Time{}, mapped
	}
	if t := tsToTimePtr(ts); t != nil {
		return *t, nil
	}
	return time.Time{}, nil
}
