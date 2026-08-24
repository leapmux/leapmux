package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type webAuthnSessionStore struct {
	conn *sqliteConn
}

var _ store.WebAuthnSessionStore = (*webAuthnSessionStore)(nil)

func fromDBWebAuthnSession(s gendb.WebauthnSession) store.WebAuthnSession {
	out := store.WebAuthnSession{
		ID:          s.ID,
		Kind:        s.Kind,
		PayloadJSON: s.PayloadJson,
		SessionData: s.SessionData,
		ExpiresAt:   s.ExpiresAt.Time,
		CreatedAt:   s.CreatedAt.Time,
	}
	if s.UserID.Valid {
		out.UserID = s.UserID.String
	}
	return out
}

func (s *webAuthnSessionStore) Create(ctx context.Context, p store.CreateWebAuthnSessionParams) error {
	var userID sql.NullString
	if p.UserID != "" {
		owner, ok := userid.New(p.UserID)
		if !ok {
			return store.ErrInvalidArgument
		}
		userID = sql.NullString{String: owner.String(), Valid: true}
	}
	return mapErr(s.conn.q.CreateWebAuthnSession(ctx, gendb.CreateWebAuthnSessionParams{
		ID:          p.ID,
		Kind:        p.Kind,
		UserID:      userID,
		PayloadJson: p.PayloadJSON,
		SessionData: p.SessionData,
		ExpiresAt:   sqltime.NewSQLiteTime(p.ExpiresAt),
		CreatedAt:   sqltime.NewSQLiteTime(p.CreatedAt),
	}))
}

func (s *webAuthnSessionStore) Get(ctx context.Context, id string) (*store.WebAuthnSession, error) {
	row, err := s.conn.q.GetWebAuthnSession(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBWebAuthnSession(row)
	return &out, nil
}

func (s *webAuthnSessionStore) Delete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.DeleteWebAuthnSession(ctx, id))
}

func (s *webAuthnSessionStore) ConsumeCeremony(ctx context.Context, id, kind string, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ConsumeWebAuthnCeremonySession(ctx, gendb.ConsumeWebAuthnCeremonySessionParams{
		ID:   id,
		Kind: kind,
		Now:  sqltime.NewSQLiteTime(now),
	}))
}

func (s *webAuthnSessionStore) ConsumeProof(ctx context.Context, id, userID, kind string, now time.Time) (int64, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ConsumeWebAuthnProof(ctx, gendb.ConsumeWebAuthnProofParams{
		ID:     id,
		Kind:   kind,
		UserID: sql.NullString{String: owner.String(), Valid: true},
		Now:    sqltime.NewSQLiteTime(now),
	}))
}

func (s *webAuthnSessionStore) GetValidProof(ctx context.Context, id, userID, kind string, now time.Time) (*store.WebAuthnSession, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return nil, store.ErrInvalidArgument
	}
	row, err := s.conn.q.GetValidWebAuthnProof(ctx, gendb.GetValidWebAuthnProofParams{
		ID:     id,
		Kind:   kind,
		UserID: sql.NullString{String: owner.String(), Valid: true},
		Now:    sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBWebAuthnSession(row)
	return &out, nil
}

func (s *webAuthnSessionStore) DeleteAllByUser(ctx context.Context, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteWebAuthnSessionsByUser(ctx, sql.NullString{
		String: owner.String(),
		Valid:  true,
	}))
}

func (s *webAuthnSessionStore) DeleteByUserAndKind(ctx context.Context, userID, kind string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteWebAuthnSessionsByUserAndKind(ctx, gendb.DeleteWebAuthnSessionsByUserAndKindParams{
		UserID: sql.NullString{String: owner.String(), Valid: true},
		Kind:   kind,
	}))
}
