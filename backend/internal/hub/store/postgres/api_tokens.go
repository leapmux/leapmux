package postgres

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type apiTokenStore struct{ conn *pgConn }

var _ store.APITokenStore = (*apiTokenStore)(nil)

func fromDBAPIToken(t gendb.ApiToken) store.APIToken {
	return store.APIToken{
		ID:                       t.ID,
		UserID:                   t.UserID,
		ClientType:               t.ClientType,
		ClientName:               t.ClientName,
		SecretHash:               t.SecretHash,
		RefreshHash:              t.RefreshHash,
		PreviousRefreshHash:      t.PreviousRefreshHash,
		PreviousRefreshExpiresAt: tsToTimePtr(t.PreviousRefreshExpiresAt),
		Scope:                    t.Scope,
		CreatedAt:                tsToTime(t.CreatedAt),
		AuthGeneration:           t.AuthGeneration,
		LastUsedAt:               tsToTimePtr(t.LastUsedAt),
		LastRotatedAt:            tsToTimePtr(t.LastRotatedAt),
		ExpiresAt:                tsToTimePtr(t.ExpiresAt),
		RefreshExpiresAt:         tsToTimePtr(t.RefreshExpiresAt),
		RevokedAt:                tsToTimePtr(t.RevokedAt),
	}
}

func (s *apiTokenStore) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	return (&pgStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*pgStore).conn.q.CreateAPIToken(ctx, gendb.CreateAPITokenParams{
			ID:               p.ID,
			UserID:           p.UserID,
			ClientType:       p.ClientType,
			ClientName:       p.ClientName,
			SecretHash:       p.SecretHash,
			RefreshHash:      p.RefreshHash,
			Scope:            p.Scope,
			ExpiresAt:        timePtrToTs(p.ExpiresAt),
			RefreshExpiresAt: timePtrToTs(p.RefreshExpiresAt),
		}))
	})
}

func (s *apiTokenStore) GetByID(ctx context.Context, id string) (*store.APIToken, error) {
	t, err := s.conn.q.GetAPITokenByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBAPIToken(t)
	return &out, nil
}

func (s *apiTokenStore) ListByUser(ctx context.Context, p store.ListAPITokensByUserParams) ([]store.APIToken, error) {
	rows, err := s.conn.q.ListAPITokensByUser(ctx, gendb.ListAPITokensByUserParams{
		UserID:     p.UserID,
		ClientType: p.ClientType,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBAPIToken), nil
}

func (s *apiTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchAPIToken(ctx, id))
}

func (s *apiTokenStore) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.RotateAPITokenRefresh(ctx, gendb.RotateAPITokenRefreshParams{
			ID:                   p.ID,
			NewSecretHash:        p.NewSecretHash,
			NewExpiresAt:         timePtrToTs(p.NewExpiresAt),
			NewRefreshHash:       p.NewRefreshHash,
			NewRefreshExpiresAt:  timePtrToTs(p.NewRefreshExpiresAt),
			PrevRefreshHash:      p.PreviousRefreshHash,
			PrevRefreshExpiresAt: timePtrToTs(p.PreviousRefreshExpiresAt),
		})
		if err != nil || n == 0 {
			return nil, mapErr(err)
		}
		if n != 1 {
			return nil, fmt.Errorf("rotate API token %q: updated %d rows", p.ID, n)
		}
		row, err := conn.q.GetAPITokenByID(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		rotatedAt, err := sqlutil.RequireTime(row.LastRotatedAt.Time, row.LastRotatedAt.Valid, "last_rotated_at")
		if err != nil {
			return nil, err
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindAPITokenRotation, SubjectID: row.ID, UserID: row.UserID, At: rotatedAt}, nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeAPIToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindAPIToken, err)
	}, emitCredentialEvent)
}

func (s *apiTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	n, err := s.conn.q.RevokeAPITokensByUserFast(ctx, userID)
	return n, mapErr(err)
}
