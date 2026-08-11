// Package testutil provides shared test helpers for hub packages.
package testutil

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/sections"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// OpenTestStore opens an in-memory SQLite store with migrations applied.
// (sqlite.Open runs migrations automatically.)
func OpenTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestAdminUsername and TestAdminPassword are the credentials created by
// CreateTestAdmin. Exported so service and e2e tests that log in as the
// fixture don't hardcode the strings in multiple places.
const (
	TestAdminUsername = usernames.Admin
	TestAdminPassword = "admin123"
)

// Argon2id is intentionally slow. Hash the fixture password once per process
// so tests that seed the admin user don't each pay ~200ms.
var (
	testAdminHashOnce sync.Once
	testAdminHash     string
	testAdminHashErr  error
)

func cachedTestAdminHash() (string, error) {
	testAdminHashOnce.Do(func() {
		testAdminHash, testAdminHashErr = password.Hash(TestAdminPassword)
	})
	return testAdminHash, testAdminHashErr
}

// CreateTestAdmin creates the default admin fixture directly via the store,
// bypassing the SignUp RPC (and therefore its reserved-username check).
func CreateTestAdmin(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	hash, err := cachedTestAdminHash()
	require.NoError(t, err)

	userID := id.Generate()

	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		Username:     TestAdminUsername,
		PasswordHash: hash,
		DisplayName:  "Admin",
		Email:        "",
		PasswordSet:  true,
		IsAdmin:      true,
	}))
	seedDefaultSections(t, st, userID)
}

// seedDefaultSections gives a fixture user the same sidebar a real one gets.
//
// The default sections are written in the SAME transaction as the user row by
// service.CreateUser, and nothing backfills them afterwards (ListSections is a
// pure read), so a fixture that creates its user through the store would have
// an empty sidebar no production user ever has -- and any test that touched
// sections would measure the fixture's gap instead of the code.
//
// The fixture cannot call service.CreateUser directly: the service layer's own
// tests import this package, so the import would be a cycle. Both call
// sections.InitDefaults instead, which is why that package sits below the
// service layer.
func seedDefaultSections(t *testing.T, st store.Store, userID string) {
	t.Helper()
	owner, ok := userid.New(userID)
	require.True(t, ok, "generated user id must be non-empty")
	require.NoError(t, sections.InitDefaults(context.Background(), st, owner))
}

// CreateTestUser creates a non-admin user with the given credentials.
// Mirrors CreateTestAdmin but with IsAdmin=false and the supplied
// password instead of the cached fixture. Useful for cross-user tests.
func CreateTestUser(t *testing.T, st store.Store, username, plainPassword string) string {
	t.Helper()
	ctx := context.Background()

	hash, err := password.Hash(plainPassword)
	require.NoError(t, err)

	userID := id.Generate()

	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		PasswordSet:  true,
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
