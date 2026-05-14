package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
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
	rows, err := s.store.WorkspaceTabIndex().ListOwnedByWorker(ctx, w.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list owned tabs: %w", err))
	}
	out := make([]*leapmuxv1.OwnedTab, 0, len(rows))
	for _, r := range rows {
		out = append(out, &leapmuxv1.OwnedTab{
			OrgId:       r.OrgID,
			WorkspaceId: r.WorkspaceID,
			TabType:     r.TabType,
			TabId:       r.TabID,
			TileId:      r.TileID,
			Position:    r.Position,
		})
	}
	return connect.NewResponse(&leapmuxv1.ListOwnedTabsForWorkerResponse{Tabs: out}), nil
}
