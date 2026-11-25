package service

import (
	"context"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// CheckNotificationWorkspace verifies that the destination belongs to the
// calling Worker's owner. The notification itself travels on the Worker stream.
func (s *WorkerConnectorService) CheckNotificationWorkspace(ctx context.Context, request *leapmuxv1.CheckNotificationWorkspaceRequest) (*emptypb.Empty, error) {
	_, owner, err := s.authenticateWorkerOwner(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID := request.GetWorkspaceId()
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a workspace ID is required")
	}
	if _, err := loadOwnedWorkspaceOrNotFound(
		ctx, s.store, workspaceID, owner,
		connect.NewError(connect.CodeNotFound, "the Worker owner has no such workspace"),
	); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
