package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type sessionStore struct{ conn *mysqlConn }

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
	return (&mysqlStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*mysqlStore).conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
			ID:        p.ID,
			UserID:    p.UserID.String(),
			ExpiresAt: sqltime.NewMySQLTime(p.ExpiresAt),
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
		ExpiresAt:    sqltime.NewMySQLTime(p.ExpiresAt),
		ID:           p.ID,
		LastActiveAt: sqltime.NewMySQLTime(p.LastActiveAt),
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

func (s *sessionStore) DeleteByUser(ctx context.Context, userID userid.UserID) error {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller names no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success
		// having changed nothing. See store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteUserSessionsByUser(ctx, owner))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: owner,
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
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    owner,
	}))
}

func (s *sessionStore) ListByUserID(ctx context.Context, p store.ListUserSessionsParams) (store.Page[store.UserSession], error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return store.Page[store.UserSession]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListUserSessionsByUserIDParams, error) {
			return listUserSessionsParams(owner, p.Cursor, p.Limit)
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
