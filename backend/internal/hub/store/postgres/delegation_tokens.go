package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type delegationTokenStore struct{ conn *pgConn }

var _ store.DelegationTokenStore = (*delegationTokenStore)(nil)

func fromDBDelegationToken(t gendb.DelegationToken) store.DelegationToken {
	return store.DelegationToken{
		ID:               t.ID,
		UserID:           t.UserID,
		WorkerID:         t.WorkerID,
		WorkspaceID:      t.WorkspaceID,
		AgentID:          t.AgentID,
		TerminalID:       t.TerminalID,
		IssuedForTabID:   t.IssuedForTabID,
		IssuedForTabType: t.IssuedForTabType,
		SecretHash:       t.SecretHash,
		RefreshHash:      t.RefreshHash,
		CreatedAt:        tsToTime(t.CreatedAt),
		AuthGeneration:   t.AuthGeneration,
		LastUsedAt:       tsToTimePtr(t.LastUsedAt),
		ExpiresAt:        tsToTime(t.ExpiresAt),
		RefreshExpiresAt: tsToTimePtr(t.RefreshExpiresAt),
		RevokedAt:        tsToTimePtr(t.RevokedAt),
	}
}

func (s *delegationTokenStore) Create(ctx context.Context, p store.CreateDelegationTokenParams) error {
	return (&pgStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*pgStore).conn.q.CreateDelegationToken(ctx, gendb.CreateDelegationTokenParams{
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
	})
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
	return store.MapSlice(rows, fromDBDelegationToken), nil
}

func (s *delegationTokenStore) ListActiveByUser(ctx context.Context, userID string) ([]store.DelegationToken, error) {
	rows, err := s.conn.q.ListActiveDelegationTokensByUser(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBDelegationToken), nil
}

func (s *delegationTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchDelegationToken(ctx, id))
}

func (s *delegationTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeDelegationToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindDelegationToken, err)
	}, emitCredentialEvent)
}

func (s *delegationTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	n, err := s.conn.q.RevokeDelegationTokensByUserFast(ctx, userID)
	return n, mapErr(err)
}
