package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

type orgTestEnv struct {
	client leapmuxv1connect.OrgServiceClient
	store  store.Store
	token  string
	userID string
	orgID  string // personal org ID of the admin user
}

func setupOrgTestServer(t *testing.T) *orgTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	cfg := testConfig()

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, false, false, false)
	opts := connect.WithInterceptors(interceptor)

	orgSvc := service.NewOrgService(st, false)
	orgPath, orgHandler := leapmuxv1connect.NewOrgServiceHandler(orgSvc, opts)
	mux.Handle(orgPath, orgHandler)

	authSvc := service.NewAuthService(st, cfg, nil, nil)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewOrgServiceClient(server.Client(), server.URL)

	// Login as the bootstrapped admin user.
	token, user, _, err := auth.Login(context.Background(), st, "admin", "admin123")
	require.NoError(t, err)

	return &orgTestEnv{
		client: client,
		store:  st,
		token:  token,
		userID: user.ID,
		orgID:  user.OrgID,
	}
}

func (e *orgTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = e.store.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		OrgID:        e.orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// Add as org member with MEMBER role in the admin's personal org.
	_ = e.store.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
		OrgID:  e.orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, _, err := auth.Login(ctx, e.store, "user2", "testpass2")
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

	assert.NotEmpty(t, resp.Msg.GetOrgId())
}

func TestOrgService_CreateOrg_DuplicateName(t *testing.T) {
	env := setupOrgTestServer(t)

	_, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "duplicate-org",
	}, env.token))
	require.NoError(t, err)

	// Creating another org with the same name should return AlreadyExists.
	_, err = env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "duplicate-org",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

	// Create second user (in personal org).
	env.createSecondUser(t)

	// Invite user2 to the new org as admin.
	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
	}, env.token))
	require.NoError(t, err)

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
	orgID := createResp.Msg.GetOrgId()

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
	hash, _ := password.Hash("pass3")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user3ID,
		OrgID:        env.orgID,
		Username:     "user3",
		PasswordHash: hash,
		DisplayName:  "User 3",
		PasswordSet:  true,
		IsAdmin:      false,
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
	orgID := createResp.Msg.GetOrgId()

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
	orgID := createResp.Msg.GetOrgId()

	// Try to remove the admin (last owner).
	_, err = env.client.RemoveOrgMember(context.Background(), authedReq(&leapmuxv1.RemoveOrgMemberRequest{
		OrgId:  orgID,
		UserId: env.userID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestOrgService_RemoveOrgMember_PreservesGrantsInOtherOrgs(t *testing.T) {
	env := setupOrgTestServer(t)
	ctx := context.Background()

	// Create two orgs.
	respA, err := env.client.CreateOrg(ctx, authedReq(&leapmuxv1.CreateOrgRequest{Name: "org-alpha"}, env.token))
	require.NoError(t, err)
	orgA := respA.Msg.GetOrgId()

	respB, err := env.client.CreateOrg(ctx, authedReq(&leapmuxv1.CreateOrgRequest{Name: "org-beta"}, env.token))
	require.NoError(t, err)
	orgB := respB.Msg.GetOrgId()

	// Create user2 and invite into both orgs.
	user2ID, _ := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(ctx, authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId: orgA, Username: "user2", Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	_, err = env.client.InviteOrgMember(ctx, authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId: orgB, Username: "user2", Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// Create a worker registered by admin (who is a member of both orgs).
	workerA := id.Generate()
	_ = env.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID: workerA, AuthToken: id.Generate(), RegisteredBy: env.userID,
		PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
	})
	// Admin is in org-alpha, so this worker is "in" org-alpha.

	workerB := id.Generate()
	_ = env.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID: workerB, AuthToken: id.Generate(), RegisteredBy: env.userID,
		PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
	})
	// Admin is also in org-beta, so this worker is "in" org-beta too.
	// For the test, we need workers scoped to specific orgs. Create a
	// user3 who is only in org-beta, and register a worker as user3.
	user3ID := id.Generate()
	hash, _ := password.Hash("pass3")
	_ = env.store.Users().Create(ctx, store.CreateUserParams{
		ID: user3ID, OrgID: env.orgID, Username: "user3",
		PasswordHash: hash, DisplayName: "User 3", PasswordSet: true,
	})
	_ = env.store.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
		OrgID: orgB, UserID: user3ID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	workerInB := id.Generate()
	_ = env.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID: workerInB, AuthToken: id.Generate(), RegisteredBy: user3ID,
		PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
	})

	// Grant user2 access to workerA (admin's worker, in org-alpha) and workerInB (user3's worker, in org-beta).
	_ = env.store.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
		WorkerID: workerA, UserID: user2ID, GrantedBy: env.userID,
	})
	_ = env.store.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
		WorkerID: workerInB, UserID: user2ID, GrantedBy: user3ID,
	})

	// Verify user2 has access to both workers.
	hasA, _ := env.store.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{WorkerID: workerA, UserID: user2ID})
	assert.True(t, hasA)
	hasB, _ := env.store.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{WorkerID: workerInB, UserID: user2ID})
	assert.True(t, hasB)

	// Remove user2 from org-alpha only.
	_, err = env.client.RemoveOrgMember(ctx, authedReq(&leapmuxv1.RemoveOrgMemberRequest{
		OrgId: orgA, UserId: user2ID,
	}, env.token))
	require.NoError(t, err)

	// User2's grant to workerA (in org-alpha) should be revoked.
	hasA, _ = env.store.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{WorkerID: workerA, UserID: user2ID})
	assert.False(t, hasA, "grant to worker in org-alpha should be revoked")

	// User2's grant to workerInB (in org-beta) should be PRESERVED.
	hasB, _ = env.store.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{WorkerID: workerInB, UserID: user2ID})
	assert.True(t, hasB, "grant to worker in org-beta should be preserved")
}

// --- UpdateOrgMember ---

func TestOrgService_UpdateOrgMember(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "role-change-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrgId()

	// Create and invite second user as member.
	user2ID, _ := env.createSecondUser(t)

	_, err = env.client.InviteOrgMember(context.Background(), authedReq(&leapmuxv1.InviteOrgMemberRequest{
		OrgId:    orgID,
		Username: "user2",
		Role:     leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.NoError(t, err)

	// Promote user2 to admin.
	_, err = env.client.UpdateOrgMember(context.Background(), authedReq(&leapmuxv1.UpdateOrgMemberRequest{
		OrgId:  orgID,
		UserId: user2ID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
	}, env.token))
	require.NoError(t, err)
}

func TestOrgService_UpdateOrgMember_CannotDemoteLastOwner(t *testing.T) {
	env := setupOrgTestServer(t)

	// Create an org — admin is the sole owner.
	createResp, err := env.client.CreateOrg(context.Background(), authedReq(&leapmuxv1.CreateOrgRequest{
		Name: "demote-last-owner-org",
	}, env.token))
	require.NoError(t, err)
	orgID := createResp.Msg.GetOrgId()

	// Try to demote the admin (last owner) to member.
	_, err = env.client.UpdateOrgMember(context.Background(), authedReq(&leapmuxv1.UpdateOrgMemberRequest{
		OrgId:  orgID,
		UserId: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
