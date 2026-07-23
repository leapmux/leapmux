package store

import "context"

// GetOwnedWorker implements the common GetOwned logic shared across all backends.
// It fetches the worker by ID, rejects soft-deleted rows, and requires the caller
// to be the user the Worker is registered to. A Worker only ever serves its
// registrant, so ownership is the whole rule -- there is no cross-user path.
//
// A zero caller UserID never matches: it is refused up front, and the ownership
// comparison itself goes through Matches, which additionally refuses a blank
// registered_by on the row. Today workers.registered_by is NOT NULL, so the
// blank-row half is defensive -- but SQLite accepts "" as a TEXT primary key,
// so a blank-id user (and rows owned by it) is representable, and two empty
// strings must never read as the same principal.
func GetOwnedWorker(
	ctx context.Context,
	p GetOwnedWorkerParams,
	getByID func(ctx context.Context, id string) (*Worker, error),
) (*Worker, error) {
	if p.UserID.IsZero() {
		return nil, ErrNotFound
	}
	w, err := getByID(ctx, p.WorkerID)
	if err != nil {
		return nil, err
	}
	if w.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if !p.UserID.Matches(w.RegisteredBy) {
		return nil, ErrNotFound
	}
	return w, nil
}
