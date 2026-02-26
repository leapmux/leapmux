package db_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
)

func newTestQueries(t *testing.T) *gendb.Queries {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	return gendb.New(sqlDB)
}

func makeID() string {
	return id.Generate()
}

func TestOrgs_CRUD(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	id := makeID()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: id, Name: "test-org"})
	require.NoError(t, err)

	org, err := q.GetOrgByID(ctx, id)
	require.NoError(t, err)
	if org.Name != "test-org" {
		t.Errorf("Name = %q, want %q", org.Name, "test-org")
	}

	org2, err := q.GetOrgByName(ctx, "test-org")
	require.NoError(t, err)
	if org2.ID != id {
		t.Errorf("ID = %q, want %q", org2.ID, id)
	}

	count, err := q.CountOrgs(ctx)
	require.NoError(t, err)
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestUsers_CRUD(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})

	userID := makeID()
	err := q.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "alice",
		PasswordHash: "hash123",
		DisplayName:  "Alice",
		IsAdmin:      0,
	})
	require.NoError(t, err)

	user, err := q.GetUserByID(ctx, userID)
	require.NoError(t, err)
	if user.Username != "alice" {
		t.Errorf("Username = %q, want %q", user.Username, "alice")
	}

	user2, err := q.GetUserByUsername(ctx, "alice")
	require.NoError(t, err)
	if user2.ID != userID {
		t.Errorf("ID = %q, want %q", user2.ID, userID)
	}

	users, err := q.ListUsersByOrgID(ctx, orgID)
	require.NoError(t, err)
	if len(users) != 1 {
		t.Fatalf("len(users) = %d, want 1", len(users))
	}

	err = q.UpdateUserPassword(ctx, gendb.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: "newhash",
	})
	require.NoError(t, err)
	updated, _ := q.GetUserByID(ctx, userID)
	if updated.PasswordHash != "newhash" {
		t.Errorf("PasswordHash = %q, want %q", updated.PasswordHash, "newhash")
	}
}

func TestWorkers_CRUD(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "admin",
		PasswordHash: "h", DisplayName: "Admin", IsAdmin: 1,
	})

	workerID := makeID()
	token := makeID()
	err := q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:           workerID,
		OrgID:        orgID,
		Name:         "dev-machine",
		Hostname:     "host1",
		Os:           "linux",
		Arch:         "amd64",
		AuthToken:    token,
		RegisteredBy: userID,
	})
	require.NoError(t, err)

	b, err := q.GetWorkerByID(ctx, gendb.GetWorkerByIDParams{ID: workerID, OrgID: orgID})
	require.NoError(t, err)
	if b.Name != "dev-machine" {
		t.Errorf("Name = %q, want %q", b.Name, "dev-machine")
	}

	b2, err := q.GetWorkerByAuthToken(ctx, token)
	require.NoError(t, err)
	if b2.ID != workerID {
		t.Errorf("ID = %q, want %q", b2.ID, workerID)
	}

	workers, err := q.ListWorkersByOrgID(ctx, gendb.ListWorkersByOrgIDParams{
		OrgID: orgID, Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	if len(workers) != 1 {
		t.Errorf("len = %d, want 1", len(workers))
	}

	err = q.UpdateWorkerLastSeen(ctx, workerID)
	require.NoError(t, err)

	err = q.MarkWorkerDeleted(ctx, workerID)
	require.NoError(t, err)
	// Worker still exists but with status='deleted', not visible in org list.
	workers, err = q.ListWorkersByOrgID(ctx, gendb.ListWorkersByOrgIDParams{
		OrgID: orgID, Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	if len(workers) != 0 {
		t.Errorf("len = %d, want 0 after MarkWorkerDeleted", len(workers))
	}
}

func TestRegistrations(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	// Create prerequisite records for FK constraints.
	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "admin",
		PasswordHash: "h", DisplayName: "Admin", IsAdmin: 1,
	})
	workerID := makeID()
	_ = q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID: workerID, OrgID: orgID, Name: "b",
		AuthToken: makeID(), RegisteredBy: userID,
	})

	regID := makeID()
	expires := time.Now().Add(10 * time.Minute).UTC()
	err := q.CreateRegistration(ctx, gendb.CreateRegistrationParams{
		ID:        regID,
		Hostname:  "host1",
		Os:        "darwin",
		Arch:      "arm64",
		Version:   "0.1.0",
		ExpiresAt: expires,
	})
	require.NoError(t, err)

	reg, err := q.GetRegistrationByID(ctx, regID)
	require.NoError(t, err)
	if reg.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING {
		t.Errorf("Status = %v, want %v", reg.Status, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING)
	}

	// Approve the registration with real FK references.
	err = q.ApproveRegistration(ctx, gendb.ApproveRegistrationParams{
		ID:         regID,
		WorkerID:   sql.NullString{String: workerID, Valid: true},
		ApprovedBy: sql.NullString{String: userID, Valid: true},
	})
	require.NoError(t, err)

	reg2, _ := q.GetRegistrationByID(ctx, regID)
	if reg2.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED {
		t.Errorf("Status = %v, want %v", reg2.Status, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED)
	}
}

func TestRegistrations_Expire(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	regID := makeID()
	// Set expiry in the past.
	expires := time.Now().Add(-1 * time.Minute).UTC()
	_ = q.CreateRegistration(ctx, gendb.CreateRegistrationParams{
		ID: regID, Hostname: "h", Os: "l", Arch: "a", Version: "v",
		ExpiresAt: expires,
	})

	err := q.ExpireRegistrations(ctx)
	require.NoError(t, err)

	reg, _ := q.GetRegistrationByID(ctx, regID)
	if reg.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED {
		t.Errorf("Status = %v, want %v", reg.Status, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED)
	}
}

func TestWorkspaces_CRUD(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u",
		PasswordHash: "h", DisplayName: "U", IsAdmin: 0,
	})
	workerID := makeID()
	_ = q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID: workerID, OrgID: orgID, Name: "b",
		AuthToken: makeID(), RegisteredBy: userID,
	})

	wsID := makeID()
	err := q.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID: wsID, OrgID: orgID,
		CreatedBy: userID, Title: "Test Workspace",
	})
	require.NoError(t, err)

	_, err = q.GetWorkspaceByID(ctx, gendb.GetWorkspaceByIDParams{ID: wsID, OrgID: orgID})
	require.NoError(t, err)

	workspaces, err := q.ListWorkspacesByOrgID(ctx, gendb.ListWorkspacesByOrgIDParams{
		OrgID: orgID, Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	if len(workspaces) != 1 {
		t.Errorf("len = %d, want 1", len(workspaces))
	}
}

func TestMessages_CRUD(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	// Set up prerequisite data.
	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u",
		PasswordHash: "h", DisplayName: "U", IsAdmin: 0,
	})
	workerID := makeID()
	_ = q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID: workerID, OrgID: orgID, Name: "b",
		AuthToken: makeID(), RegisteredBy: userID,
	})
	wsID := makeID()
	_ = q.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID: wsID, OrgID: orgID,
		CreatedBy: userID, Title: "w",
	})
	agentID := makeID()
	_ = q.CreateAgent(ctx, gendb.CreateAgentParams{
		ID: agentID, WorkspaceID: wsID, WorkerID: workerID,
		Title: "agent", Model: "haiku", SystemPrompt: "",
	})

	// Create messages.
	for i := int64(1); i <= 3; i++ {
		seq, err := q.CreateMessage(ctx, gendb.CreateMessageParams{
			ID:                 makeID(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
			Content:            []byte(`{"text":"hello"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
		if err != nil {
			t.Fatalf("CreateMessage seq=%d: %v", i, err)
		}
		if seq != i {
			t.Errorf("CreateMessage returned seq=%d, want %d", seq, i)
		}
	}

	// List from seq 0.
	msgs, err := q.ListMessagesByAgentID(ctx, gendb.ListMessagesByAgentIDParams{
		AgentID: agentID, Seq: 0, Limit: 10,
	})
	require.NoError(t, err)
	if len(msgs) != 3 {
		t.Fatalf("len = %d, want 3", len(msgs))
	}
	if msgs[0].Seq != 1 || msgs[2].Seq != 3 {
		t.Errorf("ordering wrong: first=%d, last=%d", msgs[0].Seq, msgs[2].Seq)
	}

	// List from seq 1 (should skip first message).
	msgs2, _ := q.ListMessagesByAgentID(ctx, gendb.ListMessagesByAgentIDParams{
		AgentID: agentID, Seq: 1, Limit: 10,
	})
	if len(msgs2) != 2 {
		t.Errorf("len from seq 1 = %d, want 2", len(msgs2))
	}
}

func TestUserSessions(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u",
		PasswordHash: "h", DisplayName: "U", IsAdmin: 0,
	})

	// Create a valid session.
	sessID := makeID()
	expires := time.Now().Add(24 * time.Hour).UTC()
	err := q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID: sessID, UserID: userID, ExpiresAt: expires,
	})
	require.NoError(t, err)

	sess, err := q.GetUserSessionByID(ctx, sessID)
	require.NoError(t, err)
	if sess.UserID != userID {
		t.Errorf("UserID = %q, want %q", sess.UserID, userID)
	}

	// Delete session.
	err = q.DeleteUserSession(ctx, sessID)
	require.NoError(t, err)
	_, err = q.GetUserSessionByID(ctx, sessID)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestUserSessions_Expired(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u",
		PasswordHash: "h", DisplayName: "U", IsAdmin: 0,
	})

	// Create an expired session.
	sessID := makeID()
	expires := time.Now().Add(-1 * time.Hour).UTC()
	_ = q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID: sessID, UserID: userID, ExpiresAt: expires,
	})

	// Should not be found (expired).
	_, err := q.GetUserSessionByID(ctx, sessID)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for expired session, got %v", err)
	}

	// Cleanup should remove it.
	err = q.DeleteExpiredUserSessions(ctx)
	require.NoError(t, err)
}

func TestUpdateAgentHomeDir(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	// Set up prerequisite data.
	orgID := makeID()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "org"})
	userID := makeID()
	_ = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u",
		PasswordHash: "h", DisplayName: "U", IsAdmin: 0,
	})
	workerID := makeID()
	_ = q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID: workerID, OrgID: orgID, Name: "b",
		AuthToken: makeID(), RegisteredBy: userID,
	})
	wsID := makeID()
	_ = q.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID: wsID, OrgID: orgID,
		CreatedBy: userID, Title: "w",
	})
	agentID := makeID()
	_ = q.CreateAgent(ctx, gendb.CreateAgentParams{
		ID: agentID, WorkspaceID: wsID, WorkerID: workerID,
		Title: "agent", Model: "haiku", SystemPrompt: "",
	})

	// Verify home_dir defaults to empty.
	agent, err := q.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.Empty(t, agent.HomeDir)

	// Update home_dir.
	err = q.UpdateAgentHomeDir(ctx, gendb.UpdateAgentHomeDirParams{
		HomeDir: "/home/alice",
		ID:      agentID,
	})
	require.NoError(t, err)

	// Verify it was persisted.
	agent, err = q.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "/home/alice", agent.HomeDir)
}
