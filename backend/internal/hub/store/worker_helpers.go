package store

import (
	"context"
)

// GetOwnedWorker implements the common GetOwned logic shared across all backends.
// It fetches the worker by ID, checks deletion status and ownership, then falls
// back to an access grant check.
func GetOwnedWorker(
	ctx context.Context,
	p GetOwnedWorkerParams,
	getByID func(ctx context.Context, id string) (*Worker, error),
	hasAccess func(ctx context.Context, workerID, userID string) (bool, error),
) (*Worker, error) {
	w, err := getByID(ctx, p.WorkerID)
	if err != nil {
		return nil, err
	}
	if w.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if w.RegisteredBy == p.UserID {
		return w, nil
	}
	ok, err := hasAccess(ctx, p.WorkerID, p.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return w, nil
}
