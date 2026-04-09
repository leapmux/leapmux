package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type registrationStore struct {
	q *gendb.Queries
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
		WorkerID:        ptrconv.NullStringToPtr(r.WorkerID),
		ApprovedBy:      ptrconv.NullStringToPtr(r.ApprovedBy),
		ExpiresAt:       r.ExpiresAt,
		CreatedAt:       r.CreatedAt,
	}
}

func (s *registrationStore) Create(ctx context.Context, p store.CreateRegistrationParams) error {
	return mapErr(s.q.CreateRegistration(ctx, gendb.CreateRegistrationParams{
		ID:              p.ID,
		Version:         p.Version,
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
		ExpiresAt:       p.ExpiresAt,
	}))
}

func (s *registrationStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistration, error) {
	r, err := s.q.GetRegistrationByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkerRegistration(r), nil
}

func (s *registrationStore) Approve(ctx context.Context, p store.ApproveRegistrationParams) error {
	return mapErr(s.q.ApproveRegistration(ctx, gendb.ApproveRegistrationParams{
		WorkerID:   ptrconv.PtrToNullString(p.WorkerID),
		ApprovedBy: ptrconv.PtrToNullString(p.ApprovedBy),
		ID:         p.ID,
	}))
}

func (s *registrationStore) ExpirePending(ctx context.Context) error {
	return mapErr(s.q.ExpireRegistrations(ctx))
}
