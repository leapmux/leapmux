package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type webAuthnSessionStore struct {
	conn *pgConn
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
	var userID pgtype.Text
	if p.UserID != "" {
		owner, ok := userid.New(p.UserID)
		if !ok {
			return store.ErrInvalidArgument
		}
		userID = pgtype.Text{String: owner.String(), Valid: true}
	}
	return mapErr(s.conn.q.CreateWebAuthnSession(ctx, gendb.CreateWebAuthnSessionParams{
		ID:          p.ID,
		Kind:        p.Kind,
		UserID:      userID,
		PayloadJson: p.PayloadJSON,
		SessionData: p.SessionData,
		ExpiresAt:   pgtime.New(p.ExpiresAt),
		CreatedAt:   pgtime.New(p.CreatedAt),
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
		Now:  pgtime.New(now),
	}))
}

func (s *webAuthnSessionStore) DeleteAllByUser(ctx context.Context, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteWebAuthnSessionsByUser(ctx, pgtype.Text{
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
		UserID: pgtype.Text{String: owner.String(), Valid: true},
		Kind:   kind,
	}))
}
