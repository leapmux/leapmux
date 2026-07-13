package store

import (
	"context"
)

// GetOwnedWorker implements the common GetOwned logic shared across all backends.
// It fetches the worker by ID, rejects soft-deleted rows, and requires the caller
// to be the user the Worker is registered to. A Worker only ever serves its
// registrant, so ownership is the whole rule -- there is no cross-user path.
//
// An empty caller UserID never matches: it is refused up front rather than left
// to the RegisteredBy comparison, so this shared cross-dialect helper cannot
// fail open on a blank-registrant row the way its auth-side siblings
// (auth.WorkerCanUse, auth.IsOwner) already refuse an empty identity. Today
// workers.registered_by is NOT NULL, so this is defensive -- but the guard keeps
// the store-side owner rule symmetric with those predicates (issue #288).
func GetOwnedWorker(
	ctx context.Context,
	p GetOwnedWorkerParams,
	getByID func(ctx context.Context, id string) (*Worker, error),
) (*Worker, error) {
	if p.UserID == "" {
		return nil, ErrNotFound
	}
	w, err := getByID(ctx, p.WorkerID)
	if err != nil {
		return nil, err
	}
	if w.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if w.RegisteredBy != p.UserID {
		return nil, ErrNotFound
	}
	return w, nil
}
