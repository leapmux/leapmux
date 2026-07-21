package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type delegationTokenStore struct{ conn *sqliteConn }

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
		IssuedForTabType: int32(t.IssuedForTabType),
		SecretHash:       t.SecretHash,
		RefreshHash:      t.RefreshHash,
		CreatedAt:        t.CreatedAt,
		AuthGeneration:   t.AuthGeneration,
		LastUsedAt:       sqlutil.NullTimePtr(t.LastUsedAt),
		ExpiresAt:        t.ExpiresAt,
		RefreshExpiresAt: sqlutil.NullTimePtr(t.RefreshExpiresAt),
		RevokedAt:        sqlutil.NullTimePtr(t.RevokedAt),
	}
}

func (s *delegationTokenStore) Create(ctx context.Context, p store.CreateDelegationTokenParams) error {
	return (&sqliteStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*sqliteStore).conn.q.CreateDelegationToken(ctx, gendb.CreateDelegationTokenParams{
			ID:               p.ID,
			UserID:           p.UserID,
			WorkerID:         p.WorkerID,
			WorkspaceID:      p.WorkspaceID,
			AgentID:          p.AgentID,
			TerminalID:       p.TerminalID,
			IssuedForTabID:   p.IssuedForTabID,
			IssuedForTabType: int64(p.IssuedForTabType),
			SecretHash:       p.SecretHash,
			RefreshHash:      p.RefreshHash,
			ExpiresAt:        sqlutil.BindTime(p.ExpiresAt),
			RefreshExpiresAt: sqlutil.BindNullTime(p.RefreshExpiresAt),
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

// delegationTokenWithOwner assembles the JOINed listing row shared by the
// ListAll and ListAllByUser query twins (mirroring workerWithOwner), so a
// field addition to DelegationTokenWithOwner edits one site instead of one
// closure per query.
func delegationTokenWithOwner(t gendb.DelegationToken, ownerUsername string, ownerDeleted bool) store.DelegationTokenWithOwner {
	return store.DelegationTokenWithOwner{DelegationToken: fromDBDelegationToken(t), OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}
}

func (s *delegationTokenStore) ListAll(ctx context.Context, p store.ListAllDelegationTokensParams) (store.Page[store.DelegationTokenWithOwner], error) {
	// The admin token listing is a 2x2 matrix over (user_id nil/set) x
	// (include_revoked false/true), dispatched to four generated queries
	// (mirroring workers.go ListAdmin). The revoked dimension is split rather
	// than an `(narg IS NULL OR revoked_at IS NULL)` probe because the live
	// listings' partial keyset indexes are only planner-eligible when the query
	// textually filters `revoked_at IS NULL`; the probe would deoptimize the
	// COMMON live path. The IncludingRevoked forensics variants intentionally
	// have no matching index -- see delegation_tokens.sql.
	switch {
	case p.UserID != nil && p.IncludeRevoked:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
				return listAllDelegationTokensByUserIncludingRevokedParams(*p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllDelegationTokensByUserIncludingRevoked,
			func(r gendb.ListAllDelegationTokensByUserIncludingRevokedRow) store.DelegationTokenWithOwner {
				return delegationTokenWithOwner(r.DelegationToken, r.OwnerUsername, r.OwnerDeleted)
			})
	case p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllDelegationTokensByUserParams, error) {
				return listAllDelegationTokensByUserParams(*p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllDelegationTokensByUser,
			func(r gendb.ListAllDelegationTokensByUserRow) store.DelegationTokenWithOwner {
				return delegationTokenWithOwner(r.DelegationToken, r.OwnerUsername, r.OwnerDeleted)
			})
	case p.IncludeRevoked:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
				return listAllDelegationTokensIncludingRevokedParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListAllDelegationTokensIncludingRevoked,
			func(r gendb.ListAllDelegationTokensIncludingRevokedRow) store.DelegationTokenWithOwner {
				return delegationTokenWithOwner(r.DelegationToken, r.OwnerUsername, r.OwnerDeleted)
			})
	default:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllDelegationTokensParams, error) {
				return listAllDelegationTokensParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListAllDelegationTokens,
			func(r gendb.ListAllDelegationTokensRow) store.DelegationTokenWithOwner {
				return delegationTokenWithOwner(r.DelegationToken, r.OwnerUsername, r.OwnerDeleted)
			})
	}
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
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeDelegationToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindDelegationToken, err)
	}, emitCredentialEvent)
}

func (s *delegationTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	return rowsAffected(s.conn.q.RevokeDelegationTokensByUserFast(ctx, userID))
}
