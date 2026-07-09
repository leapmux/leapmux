package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type sessionStore struct{ conn *mysqlConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:             s.ID,
		UserID:         s.UserID,
		ExpiresAt:      s.ExpiresAt,
		CreatedAt:      s.CreatedAt,
		LastActiveAt:   s.LastActiveAt,
		AuthGeneration: s.AuthGeneration,
		UserAgent:      s.UserAgent,
		IPAddress:      s.IpAddress,
	}
}

func fromDBSessions(rows []gendb.UserSession) []store.UserSession {
	return store.MapSlice(rows, fromDBSession)
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return (&mysqlStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*mysqlStore).conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
			ID:        p.ID,
			UserID:    p.UserID,
			ExpiresAt: p.ExpiresAt,
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
		ExpiresAt:    p.ExpiresAt,
		ID:           p.ID,
		LastActiveAt: p.LastActiveAt,
	})
	return n, mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *mysqlConn) (*store.CredentialEvent, error) {
		row, err := conn.q.GetUserSessionForUpdate(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, mapErr(err)
		}
		n, err := rowsAffected(conn.q.DeleteUserSession(ctx, id))
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("delete session %q: deleted %d rows after locking row", id, n)
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

// RefreshAuthGeneration stamps the kept session onto the user's current
// auth_generation. The UPDATE's SET touches no always-changing column, so a
// no-op re-stamp (session already at the target generation) changes zero rows;
// the shared caller (ChangePassword) asserts rows-affected == 1. That holds on
// MySQL because normalizeMySQLDSN forces CLIENT_FOUND_ROWS, so rowsAffected()
// counts MATCHED rows -- like sqlite changes() and postgres :execrows -- rather
// than CHANGED rows, and a matched row returns 1 even when its value is
// unchanged.
func (s *sessionStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshSessionAuthGenerationParams) (int64, error) {
	return rowsAffected(s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    p.UserID,
	}))
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
			CreatedAt:    r.CreatedAt,
			LastActiveAt: r.LastActiveAt,
			ExpiresAt:    r.ExpiresAt,
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
		CreatedAt:      row.CreatedAt.UTC(),
		ExpiresAt:      row.ExpiresAt.UTC(),
		AuthGeneration: row.AuthGeneration,
	}, nil
}
