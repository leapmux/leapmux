package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type sessionStore struct{ conn *pgConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:             s.ID,
		UserID:         s.UserID,
		ExpiresAt:      s.ExpiresAt.Time,
		CreatedAt:      s.CreatedAt.Time,
		LastActiveAt:   s.LastActiveAt.Time,
		AuthGeneration: s.AuthGeneration,
		UserAgent:      s.UserAgent,
		IPAddress:      s.IpAddress,
	}
}

func fromDBActiveSessionRow(r gendb.ListAllActiveSessionsRow) store.ActiveSession {
	return store.ActiveSession{
		ID:           r.ID,
		UserID:       r.UserID,
		Username:     r.Username,
		UserDeleted:  r.UserDeleted,
		CreatedAt:    r.CreatedAt.Time,
		LastActiveAt: r.LastActiveAt.Time,
		ExpiresAt:    r.ExpiresAt.Time,
		IPAddress:    r.IpAddress,
		UserAgent:    r.UserAgent,
	}
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return (&pgStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*pgStore).conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
			ID:        p.ID,
			UserID:    p.UserID,
			ExpiresAt: pgtime.New(p.ExpiresAt),
			UserAgent: p.UserAgent,
			IpAddress: p.IPAddress,
		}))
	})
}

func (s *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	sess, err := s.conn.q.GetUserSessionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) (int64, error) {
	n, err := s.conn.q.TouchUserSession(ctx, gendb.TouchUserSessionParams{
		ExpiresAt:    pgtime.New(p.ExpiresAt),
		ID:           p.ID,
		LastActiveAt: pgtime.New(p.LastActiveAt),
	})
	return n, mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.DeleteUserSession(ctx, id)
		if err != nil {
			mapped := mapErr(err)
			if errors.Is(mapped, store.ErrNotFound) {
				return nil, nil
			}
			return nil, mapped
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindSession, SubjectID: row.ID, UserID: row.UserID, At: time.Now().UTC()}, nil
	}, emitCredentialEvent)
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.conn.q.DeleteUserSessionsByUser(ctx, userID))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	return mapErr(s.conn.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: p.UserID,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshSessionAuthGenerationParams) (int64, error) {
	n, err := s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    p.UserID,
	})
	// Map to a store.* sentinel like the sqlite/mysql twins (which route through
	// rowsAffected->mapErr) so this dialect-neutral layer does not leak a raw pgx
	// error to a caller that pattern-matches store errors.
	return n, mapErr(err)
}

func (s *sessionStore) ListByUserID(ctx context.Context, p store.ListUserSessionsParams) (store.Page[store.UserSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListUserSessionsByUserIDParams, error) {
			return listUserSessionsParams(p.UserID, p.Cursor, p.Limit)
		},
		s.conn.q.ListUserSessionsByUserID, fromDBSession)
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) (store.Page[store.ActiveSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAllActiveSessionsParams, error) {
			return listAllActiveSessionsParams(p.Cursor, p.Limit)
		},
		s.conn.q.ListAllActiveSessions, fromDBActiveSessionRow)
}

func (s *sessionStore) ValidateWithUser(ctx context.Context, id string) (*store.SessionWithUser, error) {
	row, err := s.conn.q.ValidateSessionWithUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:         row.ID,
		OrgID:          row.OrgID,
		Username:       row.Username,
		IsAdmin:        row.IsAdmin,
		EmailVerified:  row.EmailVerified,
		Email:          row.Email,
		CreatedAt:      row.CreatedAt.UTC(),
		ExpiresAt:      row.ExpiresAt.UTC(),
		AuthGeneration: row.AuthGeneration,
	}, nil
}
