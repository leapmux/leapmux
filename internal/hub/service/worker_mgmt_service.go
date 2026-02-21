package service

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// WorkerManagementService implements the Hub-side service called by Frontend
// to manage worker registrations and workers.
type WorkerManagementService struct {
	queries   *db.Queries
	workerMgr *workermgr.Manager
	notifier  *notifier.Notifier
}

// NewWorkerManagementService creates a new WorkerManagementService.
func NewWorkerManagementService(q *db.Queries, mgr *workermgr.Manager, n *notifier.Notifier) *WorkerManagementService {
	return &WorkerManagementService{queries: q, workerMgr: mgr, notifier: n}
}

func (s *WorkerManagementService) ApproveRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ApproveRegistrationRequest],
) (*connect.Response[leapmuxv1.ApproveRegistrationResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	name := req.Msg.GetName()
	if err := validate.ValidateName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Resolve org ID - user picks which org to register the worker in.
	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// Look up the registration.
	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if reg.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("registration is %s, not pending", reg.Status))
	}

	// Create the worker record.
	workerID := id.Generate()
	authToken := id.Generate()

	if err := s.queries.CreateWorker(ctx, db.CreateWorkerParams{
		ID:           workerID,
		OrgID:        orgID,
		Name:         name,
		Hostname:     reg.Hostname,
		Os:           reg.Os,
		Arch:         reg.Arch,
		AuthToken:    authToken,
		RegisteredBy: user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create worker: %w", err))
	}

	// Update the registration to approved.
	if err := s.queries.ApproveRegistration(ctx, db.ApproveRegistrationParams{
		ID:         regID,
		WorkerID:   sql.NullString{String: workerID, Valid: true},
		ApprovedBy: sql.NullString{String: user.ID, Valid: true},
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("approve registration: %w", err))
	}

	// Wake up any long-polling worker waiting on this registration.
	s.workerMgr.NotifyRegistrationChange(regID)

	return connect.NewResponse(&leapmuxv1.ApproveRegistrationResponse{
		WorkerId: workerID,
	}), nil
}

func (s *WorkerManagementService) ListWorkers(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkersRequest],
) (*connect.Response[leapmuxv1.ListWorkersResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	limit := int64(50)
	offset := int64(0)
	if req.Msg.GetPage() != nil {
		if req.Msg.GetPage().GetLimit() > 0 {
			limit = int64(req.Msg.GetPage().GetLimit())
		}
		if req.Msg.GetPage().GetCursor() != "" {
			_, _ = fmt.Sscanf(req.Msg.GetPage().GetCursor(), "%d", &offset)
		}
	}

	workers, err := s.queries.ListVisibleWorkers(ctx, db.ListVisibleWorkersParams{
		UserID: user.ID,
		OrgID:  orgID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoWorkers := make([]*leapmuxv1.Worker, len(workers))
	for i, b := range workers {
		protoWorkers[i] = s.workerToProto(&b)
	}

	hasMore := int64(len(workers)) == limit
	var nextCursor string
	if hasMore {
		nextCursor = fmt.Sprintf("%d", offset+limit)
	}

	return connect.NewResponse(&leapmuxv1.ListWorkersResponse{
		Workers: protoWorkers,
		Page: &leapmuxv1.PageResponse{
			NextCursor: nextCursor,
			HasMore:    hasMore,
		},
	}), nil
}

func (s *WorkerManagementService) GetWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerRequest],
) (*connect.Response[leapmuxv1.GetWorkerResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	worker, err := s.queries.GetWorkerByIDInternal(ctx, req.Msg.GetWorkerId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify the user can see this worker.
	_, err = s.queries.GetVisibleWorker(ctx, db.GetVisibleWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerResponse{
		Worker: s.workerToProto(&worker),
	}), nil
}

func (s *WorkerManagementService) RenameWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RenameWorkerRequest],
) (*connect.Response[leapmuxv1.RenameWorkerResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	name := req.Msg.GetName()
	if err := validate.ValidateName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	result, err := s.queries.RenameWorker(ctx, db.RenameWorkerParams{
		Name:         name,
		ID:           req.Msg.GetWorkerId(),
		RegisteredBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("rename worker: %w", err))
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	return connect.NewResponse(&leapmuxv1.RenameWorkerResponse{}), nil
}

func (s *WorkerManagementService) DeregisterWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeregisterWorkerRequest],
) (*connect.Response[leapmuxv1.DeregisterWorkerResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	result, err := s.queries.DeregisterWorker(ctx, db.DeregisterWorkerParams{
		ID:           req.Msg.GetWorkerId(),
		RegisteredBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	if err := s.notifier.SendDeregister(ctx, req.Msg.GetWorkerId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send deregister: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.DeregisterWorkerResponse{}), nil
}

func (s *WorkerManagementService) GetRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetRegistrationRequest],
) (*connect.Response[leapmuxv1.GetRegistrationResponse], error) {
	_, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	status := reg.Status

	return connect.NewResponse(&leapmuxv1.GetRegistrationResponse{
		RegistrationToken: reg.ID,
		Hostname:          reg.Hostname,
		Os:                reg.Os,
		Arch:              reg.Arch,
		Version:           reg.Version,
		Status:            status,
	}), nil
}

func (s *WorkerManagementService) UpdateWorkerSharing(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateWorkerSharingRequest],
) (*connect.Response[leapmuxv1.UpdateWorkerSharingResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	newShareMode := req.Msg.GetShareMode()

	if newShareMode != leapmuxv1.ShareMode_SHARE_MODE_PRIVATE && newShareMode != leapmuxv1.ShareMode_SHARE_MODE_ORG && newShareMode != leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share_mode: %s", newShareMode))
	}

	// Capture old share state for enforcement.
	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	oldShareMode := worker.ShareMode

	var oldMemberIDs map[string]bool
	if oldShareMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		shares, err := s.queries.ListWorkerSharesByWorkerID(ctx, workerID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		oldMemberIDs = make(map[string]bool, len(shares))
		for _, sh := range shares {
			oldMemberIDs[sh.UserID] = true
		}
	}

	// Only the worker owner can change sharing.
	result, err := s.queries.UpdateWorkerShareMode(ctx, db.UpdateWorkerShareModeParams{
		ShareMode:    newShareMode,
		ID:           workerID,
		RegisteredBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("worker not found or not the owner"))
	}

	// Clear existing shares and set new ones for 'members' mode.
	if err := s.queries.ClearWorkerShares(ctx, workerID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	newMemberIDs := make(map[string]bool)
	if newShareMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		for _, userID := range req.Msg.GetUserIds() {
			newMemberIDs[userID] = true
			if err := s.queries.CreateWorkerShare(ctx, db.CreateWorkerShareParams{
				WorkerID: workerID,
				UserID:   userID,
			}); err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
		}
	}

	// Compute affected users and enforce.
	affectedUserIDs := s.computeAffectedUsers(user.ID, oldShareMode, oldMemberIDs, newShareMode, newMemberIDs, worker.OrgID)
	if len(affectedUserIDs) > 0 {
		_ = s.notifier.EnforceWorkerSharingChange(ctx, workerID, affectedUserIDs)
	}

	return connect.NewResponse(&leapmuxv1.UpdateWorkerSharingResponse{}), nil
}

// computeAffectedUsers determines which users lost access due to a sharing change.
// Returns user IDs that had access under the old mode but don't under the new mode.
// The owner is never affected.
func (s *WorkerManagementService) computeAffectedUsers(ownerID string, oldMode leapmuxv1.ShareMode, oldMembers map[string]bool, newMode leapmuxv1.ShareMode, newMembers map[string]bool, orgID string) []string {
	// If switching to "org", no one loses access (org is the broadest).
	if newMode == leapmuxv1.ShareMode_SHARE_MODE_ORG {
		return nil
	}

	// Build the set of users who had access under the old mode.
	// We don't track all org members here — instead, we look at who has
	// active workspaces on this worker and check if they still have access.
	// The notifier.EnforceWorkerSharingChange already filters by active workspaces.

	// For simplicity, we signal that "all non-owner users" might be affected
	// when narrowing access, and let the notifier do the filtering.
	switch {
	case oldMode == leapmuxv1.ShareMode_SHARE_MODE_ORG && newMode == leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		// Everyone except owner loses access. Return a sentinel.
		return []string{"*"}
	case oldMode == leapmuxv1.ShareMode_SHARE_MODE_ORG && newMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		// Everyone who's NOT in the new member list loses access.
		return []string{"*"}
	case oldMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS && newMode == leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		// All old members lose access.
		var affected []string
		for userID := range oldMembers {
			if userID != ownerID {
				affected = append(affected, userID)
			}
		}
		return affected
	case oldMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS && newMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		// Members removed from the list lose access.
		var affected []string
		for userID := range oldMembers {
			if userID != ownerID && !newMembers[userID] {
				affected = append(affected, userID)
			}
		}
		return affected
	default:
		return nil
	}
}

func (s *WorkerManagementService) ListWorkerShares(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkerSharesRequest],
) (*connect.Response[leapmuxv1.ListWorkerSharesResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()

	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify the user can see this worker.
	_, err = s.queries.GetVisibleWorker(ctx, db.GetVisibleWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var members []*leapmuxv1.ShareMember
	if worker.ShareMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		shares, err := s.queries.ListWorkerSharesByWorkerID(ctx, workerID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		members = make([]*leapmuxv1.ShareMember, len(shares))
		for i, sh := range shares {
			members[i] = &leapmuxv1.ShareMember{
				UserId:      sh.UserID,
				Username:    sh.Username,
				DisplayName: sh.DisplayName,
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.ListWorkerSharesResponse{
		ShareMode: worker.ShareMode,
		Members:   members,
	}), nil
}

func (s *WorkerManagementService) workerToProto(b *db.Worker) *leapmuxv1.Worker {
	lastSeen := ""
	if b.LastSeenAt.Valid {
		lastSeen = timefmt.Format(b.LastSeenAt.Time)
	}

	return &leapmuxv1.Worker{
		Id:           b.ID,
		OrgId:        b.OrgID,
		Name:         b.Name,
		Hostname:     b.Hostname,
		Os:           b.Os,
		Arch:         b.Arch,
		Online:       s.workerMgr.IsOnline(b.ID),
		CreatedAt:    timefmt.Format(b.CreatedAt),
		LastSeenAt:   lastSeen,
		RegisteredBy: b.RegisteredBy,
		ShareMode:    b.ShareMode,
	}
}
