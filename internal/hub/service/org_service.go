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
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// OrgService implements the leapmux.v1.OrgService ConnectRPC handler.
type OrgService struct {
	queries  *db.Queries
	notifier *notifier.Notifier
}

// NewOrgService creates a new OrgService.
func NewOrgService(q *db.Queries, n *notifier.Notifier) *OrgService {
	return &OrgService{queries: q, notifier: n}
}

func (s *OrgService) CreateOrg(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CreateOrgRequest],
) (*connect.Response[leapmuxv1.CreateOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	orgID := id.Generate()
	if err := s.queries.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       name,
		IsPersonal: 0,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create org: %w", err))
	}

	if err := s.queries.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: user.ID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("add org member: %w", err))
	}

	org, err := s.queries.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.CreateOrgResponse{
		Org: orgToProto(&org),
	}), nil
}

func (s *OrgService) GetOrg(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetOrgRequest],
) (*connect.Response[leapmuxv1.GetOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	org, err := s.queries.GetOrgByID(ctx, orgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.GetOrgResponse{
		Org: orgToProto(&org),
	}), nil
}

func (s *OrgService) UpdateOrg(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateOrgRequest],
) (*connect.Response[leapmuxv1.UpdateOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	// Verify caller is owner or admin of the org.
	member, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: user.ID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners and admins can update the organization"))
	}

	// Reject updates to personal orgs.
	org, err := s.queries.GetOrgByID(ctx, orgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if org.IsPersonal != 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot update a personal organization"))
	}

	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	if err := s.queries.UpdateOrgName(ctx, db.UpdateOrgNameParams{
		Name: name,
		ID:   orgID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update org name: %w", err))
	}

	updated, err := s.queries.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.UpdateOrgResponse{
		Org: orgToProto(&updated),
	}), nil
}

func (s *OrgService) DeleteOrg(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteOrgRequest],
) (*connect.Response[leapmuxv1.DeleteOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	// Verify caller is owner or global admin.
	if !user.IsAdmin {
		member, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners can delete the organization"))
		}
	}

	// DeleteOrg already rejects personal orgs (WHERE is_personal = 0).
	if err := s.queries.DeleteOrg(ctx, orgID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete org: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.DeleteOrgResponse{}), nil
}

func (s *OrgService) ListMyOrgs(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListMyOrgsRequest],
) (*connect.Response[leapmuxv1.ListMyOrgsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgs, err := s.queries.ListOrgsByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoOrgs := make([]*leapmuxv1.Org, len(orgs))
	for i := range orgs {
		protoOrgs[i] = orgToProto(&orgs[i])
	}

	return connect.NewResponse(&leapmuxv1.ListMyOrgsResponse{
		Orgs: protoOrgs,
	}), nil
}

func (s *OrgService) CheckOrgExists(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CheckOrgExistsRequest],
) (*connect.Response[leapmuxv1.CheckOrgExistsResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	_, err := s.queries.GetOrgByName(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return connect.NewResponse(&leapmuxv1.CheckOrgExistsResponse{
				Exists: false,
			}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.CheckOrgExistsResponse{
		Exists: true,
	}), nil
}

func (s *OrgService) ListOrgMembers(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListOrgMembersRequest],
) (*connect.Response[leapmuxv1.ListOrgMembersResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	// Verify caller is a member.
	if _, err := auth.ResolveOrgID(ctx, s.queries, user, orgID); err != nil {
		return nil, err
	}

	members, err := s.queries.ListOrgMembersByOrgID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoMembers := make([]*leapmuxv1.OrgMember, len(members))
	for i := range members {
		protoMembers[i] = orgMemberToProto(&members[i])
	}

	return connect.NewResponse(&leapmuxv1.ListOrgMembersResponse{
		Members: protoMembers,
	}), nil
}

func (s *OrgService) InviteOrgMember(
	ctx context.Context,
	req *connect.Request[leapmuxv1.InviteOrgMemberRequest],
) (*connect.Response[leapmuxv1.InviteOrgMemberResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	username := req.Msg.GetUsername()
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username is required"))
	}

	role := req.Msg.GetRole()
	if role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_UNSPECIFIED {
		role = leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER
	}

	// Verify caller is owner or admin.
	callerMember, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: user.ID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners and admins can invite members"))
	}

	// Look up the user by username.
	invitee, err := s.queries.GetUserByUsername(ctx, username)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Create the org_members row.
	if err := s.queries.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: invitee.ID,
		Role:   role,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("add org member: %w", err))
	}

	// Fetch the newly created member row for the response.
	members, err := s.queries.ListOrgMembersByOrgID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var newMember *db.ListOrgMembersByOrgIDRow
	for i := range members {
		if members[i].UserID == invitee.ID {
			newMember = &members[i]
			break
		}
	}
	if newMember == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find newly added member"))
	}

	return connect.NewResponse(&leapmuxv1.InviteOrgMemberResponse{
		Member: orgMemberToProto(newMember),
	}), nil
}

func (s *OrgService) RemoveOrgMember(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RemoveOrgMemberRequest],
) (*connect.Response[leapmuxv1.RemoveOrgMemberResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	targetUserID := req.Msg.GetUserId()
	if targetUserID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user_id is required"))
	}

	// Verify caller is owner or admin.
	callerMember, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: user.ID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners and admins can remove members"))
	}

	// Check the target member's role to prevent removing the last owner.
	targetMember, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: targetUserID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if targetMember.Role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
		ownerCount, err := s.queries.CountOrgMembersByRole(ctx, db.CountOrgMembersByRoleParams{
			OrgID: orgID,
			Role:  leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if ownerCount <= 1 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot remove the last owner"))
		}
	}

	if err := s.queries.DeleteOrgMember(ctx, db.DeleteOrgMemberParams{
		OrgID:  orgID,
		UserID: targetUserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove org member: %w", err))
	}

	// Enforce: deregister all the removed user's workers in this org
	// and terminate their workspaces on other users' workers.
	if s.notifier != nil {
		if err := s.notifier.EnforceOrgMemberRemoval(ctx, orgID, targetUserID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enforce org member removal: %w", err))
		}
	}

	return connect.NewResponse(&leapmuxv1.RemoveOrgMemberResponse{}), nil
}

func (s *OrgService) UpdateOrgMember(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateOrgMemberRequest],
) (*connect.Response[leapmuxv1.UpdateOrgMemberResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}

	targetUserID := req.Msg.GetUserId()
	if targetUserID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user_id is required"))
	}

	newRole := req.Msg.GetRole()
	if newRole == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("role is required"))
	}

	// Verify caller is owner or admin.
	callerMember, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: user.ID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && callerMember.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners and admins can update member roles"))
	}

	// If changing away from owner, prevent removing the last owner.
	targetMember, err := s.queries.GetOrgMember(ctx, db.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: targetUserID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if targetMember.Role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && newRole != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
		ownerCount, err := s.queries.CountOrgMembersByRole(ctx, db.CountOrgMembersByRoleParams{
			OrgID: orgID,
			Role:  leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if ownerCount <= 1 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot change role of the last owner"))
		}
	}

	if err := s.queries.UpdateOrgMemberRole(ctx, db.UpdateOrgMemberRoleParams{
		Role:   newRole,
		OrgID:  orgID,
		UserID: targetUserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update org member role: %w", err))
	}

	// Fetch updated member list to return the updated member.
	members, err := s.queries.ListOrgMembersByOrgID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var updatedMember *db.ListOrgMembersByOrgIDRow
	for i := range members {
		if members[i].UserID == targetUserID {
			updatedMember = &members[i]
			break
		}
	}
	if updatedMember == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find updated member"))
	}

	return connect.NewResponse(&leapmuxv1.UpdateOrgMemberResponse{
		Member: orgMemberToProto(updatedMember),
	}), nil
}

func orgToProto(o *db.Org) *leapmuxv1.Org {
	return &leapmuxv1.Org{
		Id:         o.ID,
		Name:       o.Name,
		IsPersonal: o.IsPersonal != 0,
		CreatedAt:  timefmt.Format(o.CreatedAt),
	}
}

func orgMemberToProto(m *db.ListOrgMembersByOrgIDRow) *leapmuxv1.OrgMember {
	return &leapmuxv1.OrgMember{
		UserId:      m.UserID,
		Username:    m.Username,
		DisplayName: m.DisplayName,
		Role:        m.Role,
		JoinedAt:    timefmt.Format(m.JoinedAt),
	}
}
