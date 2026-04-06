package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type sessionStore struct {
	q    *gendb.Queries
	pool *pgxpool.Pool
}

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:           s.ID,
		UserID:       s.UserID,
		ExpiresAt:    tsToTime(s.ExpiresAt),
		CreatedAt:    tsToTime(s.CreatedAt),
		LastActiveAt: tsToTime(s.LastActiveAt),
		UserAgent:    s.UserAgent,
		IPAddress:    s.IpAddress,
	}
}

func fromDBSessions(rows []gendb.UserSession) []store.UserSession {
	return sqlutil.MapSlice(rows, fromDBSession)
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return mapErr(s.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID:        p.ID,
		UserID:    p.UserID,
		ExpiresAt: timeToTs(p.ExpiresAt),
		UserAgent: p.UserAgent,
		IpAddress: p.IPAddress,
	}))
}

func (s *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	sess, err := s.q.GetUserSessionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) error {
	return mapErr(s.q.TouchUserSession(ctx, gendb.TouchUserSessionParams{
		ExpiresAt:    timeToTs(p.ExpiresAt),
		ID:           p.ID,
		LastActiveAt: timeToTs(p.LastActiveAt),
	}))
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.q.DeleteUserSession(ctx, id))
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.q.DeleteUserSessionsByUser(ctx, userID))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	return mapErr(s.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: p.UserID,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	rows, err := s.q.ListUserSessionsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBSessions(rows), nil
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	ts, err := parseCursorToTs(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAllActiveSessions(ctx, gendb.ListAllActiveSessionsParams{
		Cursor: ts,
		Limit:  int32(p.Limit),
	})
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
	row, err := s.q.ValidateSessionWithUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:        row.ID,
		OrgID:         row.OrgID,
		Username:      row.Username,
		IsAdmin:       row.IsAdmin,
		EmailVerified: row.EmailVerified,
	}, nil
}

// parseCursorToTs parses the opaque cursor string into a pgtype.Timestamptz.
// An empty cursor returns the zero value (no cursor). A non-empty cursor must
// be an RFC3339Nano-formatted timestamp.
func parseCursorToTs(cursor string) (pgtype.Timestamptz, error) {
	t, ok, err := store.ParseCursorTime(cursor)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	if !ok {
		return pgtype.Timestamptz{}, nil
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}
