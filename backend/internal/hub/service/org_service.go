package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/validate"
)

// OrgService implements the leapmux.v1.OrgService ConnectRPC handler.
type OrgService struct {
	store    store.Store
	soloMode bool
}

// NewOrgService creates a new OrgService.
func NewOrgService(st store.Store, soloMode bool) *OrgService {
	return &OrgService{store: st, soloMode: soloMode}
}

func (s *OrgService) CreateOrg(ctx context.Context, req *connect.Request[leapmuxv1.CreateOrgRequest]) (*connect.Response[leapmuxv1.CreateOrgResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization management is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	name, err := validate.SanitizeSlug("organization name", req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	orgID := id.Generate()
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: name, IsPersonal: false}); err != nil {
			return fmt.Errorf("create org: %w", err)
		}
		if err := tx.OrgMembers().Create(ctx, store.CreateOrgMemberParams{OrgID: orgID, UserID: user.ID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER}); err != nil {
			return fmt.Errorf("add org member: %w", err)
		}
		return nil
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("organization name %q is already taken", name))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.CreateOrgResponse{OrgId: orgID}), nil
}

func (s *OrgService) GetOrg(ctx context.Context, req *connect.Request[leapmuxv1.GetOrgRequest]) (*connect.Response[leapmuxv1.GetOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgID, err := auth.ResolveOrgID(ctx, s.store, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	org, err := s.store.Orgs().GetByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.GetOrgResponse{Org: orgToProto(org)}), nil
}

func (s *OrgService) UpdateOrg(ctx context.Context, req *connect.Request[leapmuxv1.UpdateOrgRequest]) (*connect.Response[leapmuxv1.UpdateOrgResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}
	if err := auth.RequireOrgAdmin(ctx, s.store, orgID, user.ID); err != nil {
		return nil, err
	}
	org, err := s.store.Orgs().GetByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if org.IsPersonal {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot update a personal organization"))
	}
	name, err := validate.SanitizeSlug("organization name", req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.store.Orgs().UpdateName(ctx, store.UpdateOrgNameParams{Name: name, ID: orgID}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update org name: %w", err))
	}
	org.Name = name
	return connect.NewResponse(&leapmuxv1.UpdateOrgResponse{Org: orgToProto(org)}), nil
}

func (s *OrgService) DeleteOrg(ctx context.Context, req *connect.Request[leapmuxv1.DeleteOrgRequest]) (*connect.Response[leapmuxv1.DeleteOrgResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization management is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}
	if !user.IsAdmin {
		member, err := s.store.OrgMembers().GetByOrgAndUser(ctx, orgID, user.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only owners can delete the organization"))
		}
	}
	if err := s.store.Orgs().SoftDeleteNonPersonal(ctx, orgID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete org: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.DeleteOrgResponse{}), nil
}

func (s *OrgService) ListMyOrgs(ctx context.Context, req *connect.Request[leapmuxv1.ListMyOrgsRequest]) (*connect.Response[leapmuxv1.ListMyOrgsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgs, err := s.store.OrgMembers().ListOrgsByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoOrgs := make([]*leapmuxv1.Org, len(orgs))
	for i := range orgs {
		protoOrgs[i] = orgToProto(&orgs[i])
	}
	return connect.NewResponse(&leapmuxv1.ListMyOrgsResponse{Orgs: protoOrgs}), nil
}

func (s *OrgService) CheckOrgExists(ctx context.Context, req *connect.Request[leapmuxv1.CheckOrgExistsRequest]) (*connect.Response[leapmuxv1.CheckOrgExistsResponse], error) {
	name, err := validate.SanitizeSlug("organization name", req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	_, err = s.store.Orgs().GetByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewResponse(&leapmuxv1.CheckOrgExistsResponse{Exists: false}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.CheckOrgExistsResponse{Exists: true}), nil
}

func (s *OrgService) ListOrgMembers(ctx context.Context, req *connect.Request[leapmuxv1.ListOrgMembersRequest]) (*connect.Response[leapmuxv1.ListOrgMembersResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}
	if _, err := auth.ResolveOrgID(ctx, s.store, user, orgID); err != nil {
		return nil, err
	}
	members, err := s.store.OrgMembers().ListByOrgID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoMembers := make([]*leapmuxv1.OrgMember, len(members))
	for i := range members {
		protoMembers[i] = orgMemberToProto(&members[i])
	}
	return connect.NewResponse(&leapmuxv1.ListOrgMembersResponse{Members: protoMembers}), nil
}

func (s *OrgService) InviteOrgMember(ctx context.Context, req *connect.Request[leapmuxv1.InviteOrgMemberRequest]) (*connect.Response[leapmuxv1.InviteOrgMemberResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization management is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org_id is required"))
	}
	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	role := req.Msg.GetRole()
	if role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_UNSPECIFIED {
		role = leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER
	}
	if err := auth.RequireOrgAdmin(ctx, s.store, orgID, user.ID); err != nil {
		return nil, err
	}
	invitee, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.store.OrgMembers().Create(ctx, store.CreateOrgMemberParams{OrgID: orgID, UserID: invitee.ID, Role: role}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("add org member: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.InviteOrgMemberResponse{}), nil
}

func (s *OrgService) RemoveOrgMember(ctx context.Context, req *connect.Request[leapmuxv1.RemoveOrgMemberRequest]) (*connect.Response[leapmuxv1.RemoveOrgMemberResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization management is not available in solo mode"))
	}
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
	if err := auth.RequireOrgAdmin(ctx, s.store, orgID, user.ID); err != nil {
		return nil, err
	}
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		targetMember, err := tx.OrgMembers().GetByOrgAndUser(ctx, orgID, targetUserID)
		if err != nil {
			return err
		}
		if targetMember.Role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
			ownerCount, err := tx.OrgMembers().CountByRole(ctx, store.CountOrgMembersByRoleParams{OrgID: orgID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER})
			if err != nil {
				return err
			}
			if ownerCount <= 1 {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot remove the last owner"))
			}
		}
		if err := tx.OrgMembers().Delete(ctx, store.DeleteOrgMemberParams{OrgID: orgID, UserID: targetUserID}); err != nil {
			return fmt.Errorf("remove org member: %w", err)
		}
		if err := tx.WorkerAccessGrants().DeleteByUserInOrg(ctx, store.DeleteWorkerAccessGrantsByUserInOrgParams{UserID: targetUserID, OrgID: orgID}); err != nil {
			return fmt.Errorf("revoke worker access grants: %w", err)
		}
		return nil
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.RemoveOrgMemberResponse{}), nil
}

func (s *OrgService) UpdateOrgMember(ctx context.Context, req *connect.Request[leapmuxv1.UpdateOrgMemberRequest]) (*connect.Response[leapmuxv1.UpdateOrgMemberResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization management is not available in solo mode"))
	}
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
	if err := auth.RequireOrgAdmin(ctx, s.store, orgID, user.ID); err != nil {
		return nil, err
	}
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		targetMember, err := tx.OrgMembers().GetByOrgAndUser(ctx, orgID, targetUserID)
		if err != nil {
			return err
		}
		if targetMember.Role == leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && newRole != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER {
			ownerCount, err := tx.OrgMembers().CountByRole(ctx, store.CountOrgMembersByRoleParams{OrgID: orgID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER})
			if err != nil {
				return err
			}
			if ownerCount <= 1 {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot change role of the last owner"))
			}
		}
		if err := tx.OrgMembers().UpdateRole(ctx, store.UpdateOrgMemberRoleParams{Role: newRole, OrgID: orgID, UserID: targetUserID}); err != nil {
			return fmt.Errorf("update org member role: %w", err)
		}
		return nil
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.UpdateOrgMemberResponse{}), nil
}

func orgToProto(o *store.Org) *leapmuxv1.Org {
	return &leapmuxv1.Org{
		Id:         o.ID,
		Name:       o.Name,
		IsPersonal: o.IsPersonal,
		CreatedAt:  timefmt.Format(o.CreatedAt),
	}
}

func orgMemberToProto(m *store.OrgMemberWithUser) *leapmuxv1.OrgMember {
	return &leapmuxv1.OrgMember{
		UserId:      m.UserID,
		Username:    m.Username,
		DisplayName: m.DisplayName,
		Role:        m.Role,
		JoinedAt:    timefmt.Format(m.JoinedAt),
	}
}
