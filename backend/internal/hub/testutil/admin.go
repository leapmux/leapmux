// Package testutil provides shared test helpers for hub packages.
package testutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/sections"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// OpenTestStore opens an in-memory SQLite store and applies migrations.
// Each transaction callback runs twice because server databases can retry a whole transaction after serialization conflicts.
// SQLite does not retry by itself. DoubleRunStore makes callback state accumulation fail in ordinary tests.
// See storetest.DoubleRunStore for rollback and commit behavior.
// Do not remove the wrapper to bypass a failure.
func OpenTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return storetest.NewDoubleRunStore(st)
}

// TestAdminUsername and TestAdminPassword hold the credentials that CreateTestAdmin uses.
// Shared fixture credentials keep Go tests consistent.
const (
	TestAdminUsername = usernames.Admin
	TestAdminPassword = "admin123"
)

// CreateTestAdmin creates an administrator directly through the store.
// This bypasses the SignUp RPC and its reserved-username check.
func CreateTestAdmin(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	hash := FixturePasswordHash(t, TestAdminPassword)

	userID := id.Generate()

	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:                    userID,
		Username:              TestAdminUsername,
		PasswordHash:          hash,
		DisplayName:           "Admin",
		Email:                 "",
		FirstCredentialExempt: true,
		IsAdmin:               true,
	}))
	seedDefaultSections(t, st, userID)
}

// seedDefaultSections gives each fixture user the same sidebar as a production user.
// Production account creation writes default sections with the user. ListSections never creates them.
// Fixtures must create these sections also, or sidebar tests use a state that production never creates.
// This package cannot call service.CreateUser because the service tests import it.
// Both paths call sections.InitDefaults to prevent an import cycle.
func seedDefaultSections(t *testing.T, st store.Store, userID string) {
	t.Helper()
	owner, ok := userid.New(userID)
	require.True(t, ok, "generated user id must be non-empty")
	require.NoError(t, sections.InitDefaults(context.Background(), st, owner))
}

// CreateTestUser creates a non-admin user with the supplied credentials.
// Both user helpers reuse full-cost password hashes for fixtures.
func CreateTestUser(t *testing.T, st store.Store, username, plainPassword string) string {
	t.Helper()
	ctx := context.Background()

	hash := FixturePasswordHash(t, plainPassword)

	userID := id.Generate()

	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:                    userID,
		Username:              username,
		PasswordHash:          hash,
		DisplayName:           username,
		FirstCredentialExempt: true,
	}))
	seedDefaultSections(t, st, userID)
	return userID
}

// SessionFromCookie extracts the session ID from a Set-Cookie header value.
func SessionFromCookie(t *testing.T, setCookie string) string {
	t.Helper()
	require.NotEmpty(t, setCookie, "Set-Cookie header must be present")
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, auth.CookieName+"=") {
			return strings.TrimPrefix(part, auth.CookieName+"=")
		}
	}
	t.Fatalf("session cookie %q not found in Set-Cookie: %s", auth.CookieName, setCookie)
	return ""
}
