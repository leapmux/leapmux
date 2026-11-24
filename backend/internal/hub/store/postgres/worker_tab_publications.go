package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type workerTabPublicationStore struct{ conn *pgConn }

func (s *workerTabPublicationStore) Get(ctx context.Context, key store.WorkerTabPublicationKey) (*store.WorkerTabPublication, error) {
	owner, ok := userid.OwnerFilter(key.UserID)
	if !ok || key.WorkerID == "" || key.TabID == "" {
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetWorkerTabPublication(ctx, gendb.GetWorkerTabPublicationParams{UserID: owner, WorkerID: key.WorkerID, TabID: key.TabID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, mapErr(err)
	}
	responsePayload, err := s.conn.ks.OpenBytes(row.ResponsePayload, sqlutil.WorkerTabPublicationResponseAAD(owner, key.WorkerID, key.TabID))
	if err != nil {
		return nil, err
	}
	return &store.WorkerTabPublication{RequestHash: row.RequestHash, ResponsePayload: responsePayload}, nil
}

func (s *workerTabPublicationStore) Insert(ctx context.Context, p store.InsertWorkerTabPublicationParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	userID := p.UserID.String()
	responsePayload, err := s.conn.ks.SealBytes(p.ResponsePayload, sqlutil.WorkerTabPublicationResponseAAD(userID, p.WorkerID, p.TabID))
	if err != nil {
		return err
	}
	err = s.conn.q.InsertWorkerTabPublication(ctx, gendb.InsertWorkerTabPublicationParams{UserID: userID, WorkerID: p.WorkerID, TabID: p.TabID, RequestHash: p.RequestHash, ResponsePayload: responsePayload})
	return mapErr(err)
}

// Replace preserves the original request identity while changing its result.
func (s *workerTabPublicationStore) Replace(ctx context.Context, p store.InsertWorkerTabPublicationParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	owner, ownerOK := userid.OwnerFilter(p.UserID)
	if !ownerOK {
		return store.ErrInvalidArgument
	}
	responsePayload, err := s.conn.ks.SealBytes(p.ResponsePayload, sqlutil.WorkerTabPublicationResponseAAD(owner, p.WorkerID, p.TabID))
	if err != nil {
		return err
	}
	changed, err := s.conn.q.ReplaceWorkerTabPublication(ctx, gendb.ReplaceWorkerTabPublicationParams{UserID: owner, WorkerID: p.WorkerID, TabID: p.TabID, RequestHash: p.RequestHash, ResponsePayload: responsePayload})
	if err != nil {
		return mapErr(err)
	}
	if changed != 0 {
		return nil
	}
	// MySQL can report zero changed rows for an identical replacement.
	previous, err := s.Get(ctx, p.WorkerTabPublicationKey)
	if err != nil {
		return err
	}
	if bytes.Equal(previous.RequestHash, p.RequestHash) && bytes.Equal(previous.ResponsePayload, p.ResponsePayload) {
		return nil
	}
	return store.ErrNotFound
}
