package store

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/userid"
)

// WorkerTabPublicationKey identifies one owner's creation on one Worker.
type WorkerTabPublicationKey struct {
	UserID   userid.UserID
	WorkerID string
	TabID    string
}

// WorkerTabPublication is the durable result, committed with its CRDT change.
type WorkerTabPublication struct {
	RequestHash     []byte
	ResponsePayload []byte
}

type InsertWorkerTabPublicationParams struct {
	WorkerTabPublicationKey
	WorkerTabPublication
}

func (p InsertWorkerTabPublicationParams) Validate() error {
	if p.UserID.IsZero() || p.WorkerID == "" || p.TabID == "" || len(p.RequestHash) != 32 || len(p.ResponsePayload) == 0 {
		return fmt.Errorf("%w: a publication requires an owner, Worker, tab, request hash, and response", ErrInvalidArgument)
	}
	return nil
}

type WorkerTabPublicationStore interface {
	Get(context.Context, WorkerTabPublicationKey) (*WorkerTabPublication, error)
	Insert(context.Context, InsertWorkerTabPublicationParams) error
	Replace(context.Context, InsertWorkerTabPublicationParams) error
}
