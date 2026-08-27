package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type sessionStore struct{ conn *pgConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:                 s.ID,
		UserID:             s.UserID,
		ExpiresAt:          s.ExpiresAt.Time,
		CreatedAt:          s.CreatedAt.Time,
		LastActiveAt:       s.LastActiveAt.Time,
		AuthGeneration:     s.AuthGeneration,
		ElevationProvenAt:  s.ElevationProvenAt.Ptr(),
		ElevationExpiresAt: s.ElevationExpiresAt.Ptr(),
		UserAgent:          s.UserAgent,
		IPAddress:          s.IpAddress,
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
			UserID:    p.UserID.String(),
			ExpiresAt: pgtime.New(p.ExpiresAt),
			UserAgent: p.UserAgent,
			IpAddress: p.IPAddress,
		}))
	})
}

func (s *sessionStore) GetByID(ctx context.Context, id string, now time.Time) (*store.UserSession, error) {
	sess, err := s.conn.q.GetUserSessionByID(ctx, gendb.GetUserSessionByIDParams{
		ID:  id,
		Now: pgtime.New(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams, now time.Time) (int64, error) {
	n, err := s.conn.q.TouchUserSession(ctx, gendb.TouchUserSessionParams{
		ExpiresAt:    pgtime.New(p.ExpiresAt),
		ID:           p.ID,
		LastActiveAt: pgtime.New(p.LastActiveAt),
		Now:          pgtime.New(now),
	})
	return n, mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return s.deleteEmitting(ctx, id, store.RevocationEventKindSession)
}

func (s *sessionStore) Revoke(ctx context.Context, id string) (int64, error) {
	return s.deleteEmitting(ctx, id, store.RevocationEventKindSessionRevoked)
}

// deleteEmitting removes one session row and records who ended it. The two
// callers differ in the event kind ALONE, so the delete stays one
// implementation and cannot drift between a sign-out and a revoke.
func (s *sessionStore) deleteEmitting(ctx context.Context, id, kind string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.DeleteUserSession(ctx, id)
		if err != nil {
			mapped := mapErr(err)
			if errors.Is(mapped, store.ErrNotFound) {
				return nil, nil
			}
			return nil, mapped
		}
		return &store.CredentialEvent{Kind: kind, SubjectID: row.ID, UserID: row.UserID, At: time.Now().UTC()}, nil
	}, emitCredentialEvent)
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID userid.UserID) error {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller specifies no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success when
		// it changed nothing. See userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteUserSessionsByUser(ctx, owner))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: owner,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshSessionAuthGenerationParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	n, err := s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    owner,
	})
	// Map to a store.* sentinel like the sqlite/mysql twins (which route through
	// rowsAffected->mapErr) so this dialect-neutral layer does not leak a raw pgx
	// error to a caller that pattern-matches store errors.
	return n, mapErr(err)
}

func (s *sessionStore) ListByUserID(ctx context.Context, p store.ListUserSessionsParams, now time.Time) (store.Page[store.UserSession], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return store.Page[store.UserSession]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListUserSessionsByUserIDParams, error) {
			return listUserSessionsParams(owner, p.Cursor, p.Limit, now)
		},
		s.conn.q.ListUserSessionsByUserID, fromDBSession)
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams, now time.Time) (store.Page[store.ActiveSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAllActiveSessionsParams, error) {
			return listAllActiveSessionsParams(p.Cursor, p.Limit, now)
		},
		s.conn.q.ListAllActiveSessions, fromDBActiveSessionRow)
}

func (s *sessionStore) ValidateWithUser(ctx context.Context, id string, now time.Time) (*store.SessionWithUser, error) {
	row, err := s.conn.q.ValidateSessionWithUser(ctx, gendb.ValidateSessionWithUserParams{
		ID:  id,
		Now: pgtime.New(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:             row.ID,
		Username:           row.Username,
		IsAdmin:            row.IsAdmin,
		EmailVerified:      row.EmailVerified,
		Email:              row.Email,
		CreatedAt:          row.CreatedAt.UTC(),
		ExpiresAt:          row.ExpiresAt.UTC(),
		AuthGeneration:     row.AuthGeneration,
		ElevationProvenAt:  row.ElevationProvenAt.Ptr(),
		ElevationExpiresAt: row.ElevationExpiresAt.Ptr(),
	}, nil
}

// Elevate, SlideElevation and DropElevation are the only writers of
// elevation_proven_at / elevation_expires_at. Elevate and DropElevation emit a user_info
// event so a cross-process UserInfo cache re-reads the deadline; the slide
// does not, because a stale SHORTER deadline fails closed.
func (s *sessionStore) Elevate(ctx context.Context, p store.ElevateSessionParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.ElevateUserSession(ctx, gendb.ElevateUserSessionParams{
			ElevationProvenAt:  pgtime.NullOf(p.ElevationProvenAt),
			ElevationExpiresAt: pgtime.NullOf(p.ClampedExpiresAt()),
			ID:                 p.SessionID,
			UserID:             owner,
			Now:                pgtime.New(now),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		if n == 0 {
			return nil, nil
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}

func (s *sessionStore) SlideElevation(ctx context.Context, p store.SlideSessionElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	n, err := s.conn.q.SlideUserSessionElevation(ctx, gendb.SlideUserSessionElevationParams{
		WindowDeadline: pgtime.NullOf(p.WindowDeadline),
		MaxTotalMicros: store.ElevationMaxTotal.Microseconds(),
		ID:             p.SessionID,
		UserID:         owner,
		Now:            pgtime.NullOf(now),
	})
	return n, mapErr(err)
}

func (s *sessionStore) DropElevation(ctx context.Context, p store.DropSessionElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.DropUserSessionElevation(ctx, gendb.DropUserSessionElevationParams{
			ID:     p.SessionID,
			UserID: owner,
		})
		if err != nil {
			return nil, mapErr(err)
		}
		if n == 0 {
			return nil, nil
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}
