package service

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type workerTabPublisher struct {
	workerID string
	owner    userid.UserID
	manager  *crdt.Manager
}

// authenticateTabPublisher derives the tenant and Worker for publication calls.
// The Worker checks the originating caller's grant before resource persistence.
func (s *WorkerConnectorService) authenticateTabPublisher(ctx context.Context, request *leapmuxv1.TabPublication) (*workerTabPublisher, error) {
	worker, err := auth.AuthenticateWorkerBearer(ctx, s.store, s.hasher, serverCallInfo(ctx).RequestHeader().Get("Authorization"))
	if err != nil {
		if auth.IsWorkerCredentialError(err) {
			return nil, wrappedConnectError(connect.CodeUnauthenticated, err)
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	owner, ok := userid.New(worker.RegisteredBy)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, "the Worker has no owner")
	}
	if err := tabcreation.Validate(request.GetCreation(), request.GetTabType()); err != nil {
		return nil, wrappedConnectError(connect.CodeInvalidArgument, err)
	}
	if s.crdtRegistry == nil {
		return nil, connect.NewError(connect.CodeInternal, "tab publication is not configured")
	}
	manager, err := s.crdtRegistry.Get(ctx, owner.String())
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return &workerTabPublisher{workerID: worker.ID, owner: owner, manager: manager}, nil
}

// catchUpFreshTabPublication applies committed workspace lifecycle inputs before
// a fresh intent can be rejected against an incomplete canonical workspace.
// A ready destination and existing receipts do not depend on unrelated rows.
func (s *WorkerConnectorService) catchUpFreshTabPublication(ctx context.Context, publisher *workerTabPublisher, request *leapmuxv1.TabPublication) error {
	_, err := s.store.WorkerTabPublications().Get(ctx, store.WorkerTabPublicationKey{UserID: publisher.owner, WorkerID: publisher.workerID, TabID: request.GetCreation().GetTabId()})
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	workspaceID := request.GetCreation().GetPlacement().GetWorkspaceId()
	workspaceReady := func() bool {
		ready := false
		publisher.manager.WithStateRLock(func(state *leapmuxv1.UserCrdtState) {
			ready = state.GetWorkspaces()[workspaceID] != nil
		})
		return ready
	}
	reader := lifecycleOutboxAdapter{store: s.store}
	for !workspaceReady() {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending, err := reader.ListPendingLifecycleOutbox(ctx, publisher.owner.String())
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		// SubmitLifecycle queues canonical seed writes itself, so it runs outside
		// the publication writer. Its successful page consumes every listed row.
		if err := publisher.manager.SubmitLifecycle(ctx, reader); err != nil && !workspaceReady() {
			return err
		}
	}
	return nil
}

// PublishTab authenticates the hosting Worker and commits its owner's tab.
func (s *WorkerConnectorService) PublishTab(ctx context.Context, request *leapmuxv1.PublishTabRequest) (*leapmuxv1.PublishTabResponse, error) {
	publication := request.GetPublication()
	publisher, err := s.authenticateTabPublisher(ctx, publication)
	if err != nil {
		return nil, err
	}
	if err := s.catchUpFreshTabPublication(ctx, publisher, publication); err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	response, err := publisher.manager.PublishTab(ctx, publisher.workerID, publication)
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return response, nil
}

// CancelTabPublication prevents a delayed creation from publishing after close.
func (s *WorkerConnectorService) CancelTabPublication(ctx context.Context, request *leapmuxv1.CancelTabPublicationRequest) (*emptypb.Empty, error) {
	publication := request.GetPublication()
	publisher, err := s.authenticateTabPublisher(ctx, publication)
	if err != nil {
		return nil, err
	}
	if err := publisher.manager.CancelTabPublication(ctx, publisher.workerID, publication); err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return &emptypb.Empty{}, nil
}

// ResolveTabPublication reads current hosting state without changing its receipt.
func (s *WorkerConnectorService) ResolveTabPublication(ctx context.Context, request *leapmuxv1.ResolveTabPublicationRequest) (*leapmuxv1.ResolveTabPublicationResponse, error) {
	publication := request.GetPublication()
	publisher, err := s.authenticateTabPublisher(ctx, publication)
	if err != nil {
		return nil, err
	}
	response, err := publisher.manager.ResolveTabPublication(ctx, publisher.workerID, publication)
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return response, nil
}
