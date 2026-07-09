package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type sessionStore struct{ conn *pgConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:             s.ID,
		UserID:         s.UserID,
		ExpiresAt:      tsToTime(s.ExpiresAt),
		CreatedAt:      tsToTime(s.CreatedAt),
		LastActiveAt:   tsToTime(s.LastActiveAt),
		AuthGeneration: s.AuthGeneration,
		UserAgent:      s.UserAgent,
		IPAddress:      s.IpAddress,
	}
}

func fromDBSessions(rows []gendb.UserSession) []store.UserSession {
	return store.MapSlice(rows, fromDBSession)
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return (&pgStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*pgStore).conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
			ID:        p.ID,
			UserID:    p.UserID,
			ExpiresAt: timeToTs(p.ExpiresAt),
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
		ExpiresAt:    timeToTs(p.ExpiresAt),
		ID:           p.ID,
		LastActiveAt: timeToTs(p.LastActiveAt),
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

func (s *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	rows, err := s.conn.q.ListUserSessionsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBSessions(rows), nil
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	params, err := listAllActiveSessionsParams(p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListAllActiveSessions(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.ActiveSession, len(rows))
	for i, r := range rows {
		out[i] = store.ActiveSession{
			ID:           r.ID,
			UserID:       r.UserID,
			Username:     r.Username,
			CreatedAt:    tsToTime(r.CreatedAt),
			LastActiveAt: tsToTime(r.LastActiveAt),
			ExpiresAt:    tsToTime(r.ExpiresAt),
			IPAddress:    r.IpAddress,
			UserAgent:    r.UserAgent,
		}
	}
	return out, nil
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
		CreatedAt:      row.CreatedAt.Time.UTC(),
		ExpiresAt:      row.ExpiresAt.Time.UTC(),
		AuthGeneration: row.AuthGeneration,
	}, nil
}
