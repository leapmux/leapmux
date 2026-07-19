package auth

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// WorkerCanUse reports whether userID is the registrant of workerID.
// A Worker serves exactly one user — the one it is registered to —
// and there is no mechanism for conveying its use to anybody else, so
// registration is the whole rule.
//
// Returns the worker record so callers can apply additional filters
// without a re-fetch. Both the channel-service path and the CRDT auth
// checker additionally require Status == ACTIVE (via WorkerUsableNow),
// so a deregistering worker -- the operator's containment action --
// can neither be opened a channel to nor have a tab bound to it.
//
// Result triples:
//   - (worker, true, nil)  — caller is the registrant; the caller may
//     still reject based on the worker's status/deletion state.
//   - (worker, false, nil) — worker exists but caller is not its
//     registrant.
//   - (nil,    false, nil) — worker missing or one of workerID/userID
//     was empty.
//   - (nil,    false, err) — store error; treat as deny.
func WorkerCanUse(ctx context.Context, st store.Store, workerID, userID string) (*store.Worker, bool, error) {
	if workerID == "" || userID == "" {
		return nil, false, nil
	}
	w, err := st.Workers().GetByID(ctx, workerID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return w, w.RegisteredBy == userID, nil
}

// WorkerUsableNow reports whether a loaded worker may be reached at the
// instant it was read -- it is ACTIVE (a deregistering worker, the operator's
// containment action, can neither be opened a channel to nor have a tab bound
// to it). It is the shared bar the channel-service entrypoints and the CRDT
// auth checker both apply after WorkerCanUse, so "what makes a worker usable
// now" is one rule rather than two that can drift apart. A soft-deleted worker
// is unreachable here not because of a DeletedAt check but because Workers()
// .GetByID filters `deleted_at IS NULL` on every dialect, so the record only
// exists for a non-deleted row -- a defense-in-depth DeletedAt check would be
// dead against that filter.
func WorkerUsableNow(w *store.Worker) bool {
	return w != nil && w.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
}
