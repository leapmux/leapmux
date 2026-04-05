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
	"github.com/leapmux/leapmux/internal/util/id"
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
		PasswordSet:  1,
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
		PasswordHash: "h", DisplayName: "Admin", PasswordSet: 1, IsAdmin: 1,
	})

	workerID := makeID()
	token := makeID()
	err := q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       token,
		RegisteredBy:    userID,
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
	})
	require.NoError(t, err)

	b, err := q.GetWorkerByID(ctx, workerID)
	require.NoError(t, err)
	if b.ID != workerID {
		t.Errorf("ID = %q, want %q", b.ID, workerID)
	}

	b2, err := q.GetWorkerByAuthToken(ctx, token)
	require.NoError(t, err)
	if b2.ID != workerID {
		t.Errorf("ID = %q, want %q", b2.ID, workerID)
	}

	workers, err := q.ListWorkersByUserID(ctx, gendb.ListWorkersByUserIDParams{
		RegisteredBy: userID, Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	if len(workers) != 1 {
		t.Errorf("len = %d, want 1", len(workers))
	}

	err = q.UpdateWorkerLastSeen(ctx, workerID)
	require.NoError(t, err)

	err = q.MarkWorkerDeleted(ctx, workerID)
	require.NoError(t, err)
	// Worker still exists but with status='deleted', not visible in user list.
	workers, err = q.ListWorkersByUserID(ctx, gendb.ListWorkersByUserIDParams{
		RegisteredBy: userID, Limit: 10, Offset: 0,
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
		PasswordHash: "h", DisplayName: "Admin", PasswordSet: 1, IsAdmin: 1,
	})
	workerID := makeID()
	_ = q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       makeID(),
		RegisteredBy:    userID,
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
	})

	regID := makeID()
	expires := time.Now().Add(10 * time.Minute).UTC()
	err := q.CreateRegistration(ctx, gendb.CreateRegistrationParams{
		ID:              regID,
		Version:         "0.1.0",
		PublicKey:       []byte("test-public-key"),
		MlkemPublicKey:  []byte("test-mlkem-key"),
		SlhdsaPublicKey: []byte("test-slhdsa-key"),
		ExpiresAt:       expires,
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
		ID: regID, Version: "v",
		PublicKey: []byte("test-key"), MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
		ExpiresAt: expires,
	})

	err := q.ExpireRegistrations(ctx)
	require.NoError(t, err)

	reg, _ := q.GetRegistrationByID(ctx, regID)
	if reg.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED {
		t.Errorf("Status = %v, want %v", reg.Status, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED)
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
		PasswordHash: "h", DisplayName: "U", PasswordSet: 1, IsAdmin: 0,
	})

	// Create a valid session.
	sessID := makeID()
	expires := time.Now().Add(24 * time.Hour).UTC()
	err := q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID: sessID, UserID: userID, ExpiresAt: expires, UserAgent: "", IpAddress: "",
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
		PasswordHash: "h", DisplayName: "U", PasswordSet: 1, IsAdmin: 0,
	})

	// Create an expired session.
	sessID := makeID()
	expires := time.Now().Add(-1 * time.Hour).UTC()
	_ = q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID: sessID, UserID: userID, ExpiresAt: expires, UserAgent: "", IpAddress: "",
	})

	// Should not be found (expired).
	_, err := q.GetUserSessionByID(ctx, sessID)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for expired session, got %v", err)
	}

	// Cleanup should remove it.
	_, err = q.DeleteExpiredUserSessions(ctx)
	require.NoError(t, err)
}
