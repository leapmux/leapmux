package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type registrationStore struct {
	conn *pgConn
}

var _ store.RegistrationStore = (*registrationStore)(nil)

func fromDBWorkerRegistration(r gendb.WorkerRegistration) *store.WorkerRegistration {
	return &store.WorkerRegistration{
		ID:              r.ID,
		Version:         r.Version,
		PublicKey:       r.PublicKey,
		MlkemPublicKey:  r.MlkemPublicKey,
		SlhdsaPublicKey: r.SlhdsaPublicKey,
		Status:          r.Status,
		WorkerID:        textToPtr(r.WorkerID),
		ApprovedBy:      textToPtr(r.ApprovedBy),
		ExpiresAt:       tsToTime(r.ExpiresAt),
		CreatedAt:       tsToTime(r.CreatedAt),
	}
}

func (s *registrationStore) Create(ctx context.Context, p store.CreateRegistrationParams) error {
	return mapErr(s.conn.q.CreateRegistration(ctx, gendb.CreateRegistrationParams{
		ID:              p.ID,
		Version:         p.Version,
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
		ExpiresAt:       timeToTs(p.ExpiresAt),
	}))
}

func (s *registrationStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistration, error) {
	r, err := s.conn.q.GetRegistrationByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkerRegistration(r), nil
}

func (s *registrationStore) Approve(ctx context.Context, p store.ApproveRegistrationParams) error {
	return mapErr(s.conn.q.ApproveRegistration(ctx, gendb.ApproveRegistrationParams{
		WorkerID:   ptrToText(p.WorkerID),
		ApprovedBy: ptrToText(p.ApprovedBy),
		ID:         p.ID,
	}))
}

func (s *registrationStore) ExpirePending(ctx context.Context) error {
	return mapErr(s.conn.q.ExpireRegistrations(ctx))
}
