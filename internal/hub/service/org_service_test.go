package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type orgTestEnv struct {
	client  leapmuxv1connect.OrgServiceClient
	queries *gendb.Queries
	token   string
	userID  string
	orgID   string // personal org ID of the admin user
}

func setupOrgTestServer(t *testing.T) *orgTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)

	err = bootstrap.Run(context.Background(), q)
	require.NoError(t, err)

	bgMgr := workermgr.New()
	pendingReqs := workermgr.NewPendingRequests()
	agentMgr := agentmgr.New()
	notifierSvc := notifier.New(q, bgMgr, pendingReqs, agentMgr)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q))

	orgSvc := service.NewOrgService(q, notifierSvc)
	orgPath, orgHandler := leapmuxv1connect.NewOrgServiceHandler(orgSvc, opts)
	mux.Handle(orgPath, orgHandler)

	authSvc := service.NewAuthService(q)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewOrgServiceClient(server.Client(), server.URL)

	// Login as the bootstrapped admin user.
	token, user, err := auth.Login(context.Background(), q, "admin", "admin")
	require.NoError(t, err)

	return &orgTestEnv{
		client:  client,
		queries: q,
		token:   token,
		userID:  user.ID,
		orgID:   user.OrgID,
	}
}

func (e *orgTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass2"), bcrypt.MinCost)
	_ = e.queries.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        e.orgID,
		Username:     "user2",
		PasswordHash: string(hash),
		DisplayName:  "User 2",
		IsAdmin:      0,
	})
	// Add as org member with MEMBER role in the admin's personal org.
	_ = e.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  e.orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, err := auth.Login(ctx, e.queries, "user2", "pass2")
	require.NoError(t, err)
	return
}

// --- CreateOrg ---

func TestOrgService_CreateOrg(t *testing.T) {
	env := setupOrgTestServer(t)

	resp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "my-team",
	}, env.token))
	require.NoError(t, err)

	org := resp.Msg.GetOrg()
	assert.NotEmpty(t, org.GetId())
	assert.Equal(t, "my-team", org.GetName())
	assert.False(t, org.GetIsPersonal())
	assert.NotEmpty(t, org.GetCreatedAt())
}

func TestOrgService_CreateOrg_EmptyName(t *testing.T) {
	env := setupOrgTestServer(t)

	_, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// --- GetOrg ---

func TestOrgService_GetOrg(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org first.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "get-test-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Get it by ID.
	resp, err := env.client.GetOrg(context.Background(), authedReq(&leapmuxv1.GetOrgRequest{
		OrgId: orgID,
	}, env.token))
	require.NoError(t, err)

	org := resp.Msg.GetOrg()
	assert.Equal(t, orgID, org.GetId())
	assert.Equal(t, "get-test-org", org.GetName())
}

func TestOrgService_GetOrg_NotFound(t *testing.T) {
	env := setupOrgTestServer(t)

	_, err := env.client.GetOrg(context.Background(), authedReq(&leapmuxv1.GetOrgRequest{
		OrgId: "nonexistent-org-id",
	}, env.token))
	require.Error(t, err)
	// ResolveOrgID returns NotFound when the user is not a member.
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- UpdateOrg ---

func TestOrgService_UpdateOrg(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "rename-me",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Rename it.
	resp, err := env.client.UpdateOrg(context.Background(), authedReq(&leapmuxv1.UpdateOrgRequest{
		OrgId: orgID,
		Name:  "renamed-org",
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, "renamed-org", resp.Msg.GetOrg().GetName())
}

func TestOrgService_UpdateOrg_PersonalOrgRejected(t *testing.T) {
	env := setupOrgTestServer(t)

	// Try to rename the personal org.
	_, err := env.client.UpdateOrg(context.Background(), authedReq(&leapmuxv1.UpdateOrgRequest{
		OrgId: env.orgID,
		Name:  "new-personal-name",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestOrgService_UpdateOrg_NotOwnerOrAdmin(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org as admin.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "admin-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create second user and invite them as a regular member.
	_, user2Token := env.createSecondUser(t)

	// Invite user2 as a member (not admin/owner).
	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// user2 tries to rename the org.
	_, err = env.client.UpdateOrg(context.Background(), authedReq(&leapmuxv1.UpdateOrgRequest{
		OrgId: orgID,
		Name:  "hijacked-name",
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// --- DeleteOrg ---

func TestOrgService_DeleteOrg(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "delete-me",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Delete it.
	_, err = env.client.DeleteOrg(context.Background(), authedReq(&leapmuxv1.DeleteOrgRequest{
		OrgId: orgID,
	}, env.token))
	require.NoError(t, err)

	// Verify it is gone by trying to get it.
	_, err = env.client.GetOrg(context.Background(), authedReq(&leapmuxv1.GetOrgRequest{
		OrgId: orgID,
	}, env.token))
	require.Error(t, err)
}

func TestOrgService_DeleteOrg_NotOwner(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org as admin.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "owner-only-delete",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create second user and invite them as a member.
	_, user2Token := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// user2 tries to delete the org.
	_, err = env.client.DeleteOrg(context.Background(), authedReq(&leapmuxv1.DeleteOrgRequest{
		OrgId: orgID,
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// --- ListMyOrgs ---

func TestOrgService_ListMyOrgs(t *testing.T) {
	env := setupOrgTestServer(t)

	// Admin already has a personal org from bootstrap.
	// Create two more orgs.
	_, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "team-alpha",
	}, env.token))
	require.NoError(t, err)

	_, err = env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "team-beta",
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListMyOrgs(context.Background(), authedReq(&leapmuxv1.ListMyOrgsRequest{}, env.token))
	require.NoError(t, err)

	// Should have personal org + 2 created orgs = 3.
	assert.Len(t, resp.Msg.GetOrgs(), 3)

	// Verify the org names are present.
	names := make(map[string]bool)
	for _, org := range resp.Msg.GetOrgs() {
		names[org.GetName()] = true
	}
	assert.True(t, names["admin"], "personal org should be listed")
	assert.True(t, names["team-alpha"], "team-alpha should be listed")
	assert.True(t, names["team-beta"], "team-beta should be listed")
}

// --- CheckOrgExists ---

func TestOrgService_CheckOrgExists_True(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	_, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "existing-org",
	}, env.token))
	require.NoError(t, err)

	// CheckOrgExists is a public procedure (no auth required).
	resp, err := env.client.CheckOrgExists(context.Background(), connect.NewRequest(&leapmuxv1.CheckOrgExistsRequest{
		Name: "existing-org",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetExists())
}

func TestOrgService_CheckOrgExists_False(t *testing.T) {
	env := setupOrgTestServer(t)

	// CheckOrgExists is a public procedure (no auth required).
	resp, err := env.client.CheckOrgExists(context.Background(), connect.NewRequest(&leapmuxv1.CheckOrgExistsRequest{
		Name: "nonexistent-org",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetExists())

	_ = env // suppress unused warning
}

// --- ListOrgMembers ---

func TestOrgService_ListOrgMembers(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org. The admin is automatically the owner.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "members-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	resp, err := env.client.ListOrgMembers(context.Background(), authedReq(&leapmuxv1.ListOrgMembersRequest{
		OrgId: orgID,
	}, env.token))
	require.NoError(t, err)

	members := resp.Msg.GetMembers()
	require.Len(t, members, 1)
	assert.Equal(t, "admin", members[0].GetUsername())
	assert.Equal(t, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER, members[0].GetRole())
}

// --- InviteOrgMember ---

func TestOrgService_InviteOrgMember(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "invite-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create second user (in personal org).
	env.createSecondUser(t)

	// Invite user2 to the new org as admin.
	inviteResp, err := env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
	}, env.token))
	require.NoError(t, err)

	member := inviteResp.Msg.GetMember()
	assert.Equal(t, "user2", member.GetUsername())
	assert.Equal(t, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN, member.GetRole())

	// Verify members list now has 2 members.
	listResp, err := env.client.ListOrgMembers(context.Background(), authedReq(&leapmuxv1.ListOrgMembersRequest{
		OrgId: orgID,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, listResp.Msg.GetMembers(), 2)
}

func TestOrgService_InviteOrgMember_NotOwnerOrAdmin(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "restricted-invite-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create second user and invite as regular member.
	_, user2Token := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// Create a third user for user2 to attempt to invite.
	user3ID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass3"), bcrypt.MinCost)
	_ = env.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           user3ID,
		OrgID:        env.orgID,
		Username:     "user3",
		PasswordHash: string(hash),
		DisplayName:  "User 3",
		IsAdmin:      0,
	})

	// user2 (member role) tries to invite user3 — should fail.
	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user3",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// --- RemoveOrgMember ---

func TestOrgService_RemoveOrgMember(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "remove-member-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create and invite second user.
	user2ID, _ := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// Remove user2.
	_, err = env.client.RemoveOrgMember(context.Background(), authedReq(&leapmuxv1.RemoveOrgMemberRequest{
		OrgId:  orgID,
		UserId: user2ID,
	}, env.token))
	require.NoError(t, err)

	// Verify only 1 member remains.
	listResp, err := env.client.ListOrgMembers(context.Background(), authedReq(&leapmuxv1.ListOrgMembersRequest{
		OrgId: orgID,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, listResp.Msg.GetMembers(), 1)
}

func TestOrgService_RemoveOrgMember_CannotRemoveLastOwner(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org — admin is the sole owner.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "last-owner-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Try to remove the admin (last owner).
	_, err = env.client.RemoveOrgMember(context.Background(), authedReq(&leapmuxv1.RemoveOrgMemberRequest{
		OrgId:  orgID,
		UserId: env.userID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// --- UpdateOrgMember ---

func TestOrgService_UpdateOrgMember(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "role-change-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Create and invite second user as member.
	user2ID, _ := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// Promote user2 to admin.
	resp, err := env.client.UpdateOrgMember(context.Background(), authedReq(&leapmuxv1.UpdateOrgMemberRequest{
		OrgId:  orgID,
		UserId: user2ID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN, resp.Msg.GetMember().GetRole())
}

func TestOrgService_UpdateOrgMember_CannotDemoteLastOwner(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org — admin is the sole owner.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "demote-last-owner-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrg().GetId()

	// Try to demote the admin (last owner) to member.
	_, err = env.client.UpdateOrgMember(context.Background(), authedReq(&leapmuxv1.UpdateOrgMemberRequest{
		OrgId:  orgID,
		UserId: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
