package service

import (
	"context"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// CheckNotificationWorkspace verifies that the destination belongs to the
// calling Worker's owner. The notification itself travels on the Worker stream.
func (s *WorkerConnectorService) CheckNotificationWorkspace(ctx context.Context, request *leapmuxv1.CheckNotificationWorkspaceRequest) (*emptypb.Empty, error) {
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
	workspaceID := request.GetWorkspaceId()
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a workspace ID is required")
	}
	_, err = loadOwnedWorkspaceOr403(ctx, s.store, workspaceID, owner, "the Worker owner has no such workspace")
	code := connect.CodeOf(err)
	if code == connect.CodePermissionDenied || code == connect.CodeNotFound {
		return nil, connect.NewError(connect.CodeNotFound, "the Worker owner has no such workspace")
	}
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
