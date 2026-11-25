package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// ListOwnedTabsForWorker resolves the calling worker via its bearer token (the
// same token the Worker's WebSocket stream uses) and returns every owned-tab row for that
// worker. It gives the periodic worker-side orphan reconciler a snapshot of
// `workspace_tab_owned` filtered to the calling worker.
func (s *WorkerConnectorService) ListOwnedTabsForWorker(
	ctx context.Context,
	req *leapmuxv1.ListOwnedTabsForWorkerRequest,
) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
	// The bearer resolves the worker AND its registrant, so the owner axis is
	// in hand here. Binding it in the query narrows what the response means,
	// and OwnerUserId states that owner once for the reconciler.
	w, registeredBy, err := s.authenticateWorkerOwner(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
		UserID:   registeredBy,
		WorkerID: w.ID,
	})
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("list owned tabs: %w", err))
	}
	// Each row carries only its tab: the query binds one owner, so OwnerUserId
	// below states every row's owner once, and the reconciler keys its
	// (tab_type, tab_id, user_id) comparison by that owner.
	out := make([]*leapmuxv1.WorkerTabState, 0, len(rows))
	for _, r := range rows {
		out = append(out, &leapmuxv1.WorkerTabState{
			Tab:          &leapmuxv1.TabIdent{TabType: r.TabType, TabId: r.TabID},
			ArchiveState: r.ArchiveState,
		})
	}
	return &leapmuxv1.ListOwnedTabsForWorkerResponse{
		TabStates:   out,
		OwnerUserId: registeredBy.String(),
	}, nil
}
