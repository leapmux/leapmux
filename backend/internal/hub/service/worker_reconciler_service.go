package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// WorkerReconcilerService implements WorkerReconcilerServiceHandler.
// Authenticated by the worker's auth_token (the same bearer used for
// Connect). Provides the periodic worker-side orphan reconciler with
// a snapshot of `workspace_tab_owned` filtered to the calling worker.
type WorkerReconcilerService struct {
	store store.Store
}

// NewWorkerReconcilerService returns a service handler.
func NewWorkerReconcilerService(st store.Store) *WorkerReconcilerService {
	return &WorkerReconcilerService{store: st}
}

// ListOwnedTabsForWorker resolves the calling worker via its bearer
// token and returns every owned-tab row for that worker.
func (s *WorkerReconcilerService) ListOwnedTabsForWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListOwnedTabsForWorkerRequest],
) (*connect.Response[leapmuxv1.ListOwnedTabsForWorkerResponse], error) {
	w, err := auth.AuthenticateWorkerBearer(ctx, s.store, req.Header().Get("Authorization"))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	// The bearer resolves the worker AND its registrant, so the owner axis is
	// in hand here. It is bound in the query, because workspace_tab_owned is
	// keyed by (user_id, tab_id) and worker_id alone selects across tenants:
	// nothing in the schema ties a row's user_id to the registrant of the
	// worker it names.
	//
	// Binding it also NARROWS what the response means, and the narrowing is
	// the point of OwnerUserId below: the reconciler on the other end reaps
	// every local row this list omits, so a list that covers one owner must
	// say so or it reads as a universal absence.
	registeredBy, ok := userid.New(w.RegisteredBy)
	if !ok {
		// A worker row with a blank registrant cannot be scoped to an owner,
		// so there is no answer to give -- and an empty, unscoped response is
		// the one thing the reconciler must not act on.
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("worker %s has a blank registrant", w.ID))
	}
	rows, err := s.store.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
		UserID:   registeredBy,
		WorkerID: w.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list owned tabs: %w", err))
	}
	out := make([]*leapmuxv1.OwnedTab, 0, len(rows))
	for _, r := range rows {
		// UserId is redundant with OwnerUserId today (the query binds one
		// owner) but stays per-row: it is what the reconciler keys its
		// (tab_type, tab_id, user_id) comparison by, since a FILE tab id is
		// unique only within a user.
		out = append(out, &leapmuxv1.OwnedTab{
			TabType: r.TabType,
			TabId:   r.TabID,
			UserId:  r.UserID,
		})
	}
	return connect.NewResponse(&leapmuxv1.ListOwnedTabsForWorkerResponse{
		Tabs:        out,
		OwnerUserId: registeredBy.String(),
	}), nil
}
