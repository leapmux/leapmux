package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// Suite is the conformance test suite for the store abstraction layer.
// Each backend provides a NewStore function and calls Suite.Run.
type Suite struct {
	NewStore func(t *testing.T) store.TestableStore
	// ConcurrentWriteTransactions reports whether this backend can hold two
	// write transactions open at once. Every server backend can. SQLite
	// cannot: it is an embedded, single-writer database, and a test that
	// opens a second write transaction while the first is parked simply
	// deadlocks -- there is no conflict to resolve because there is no
	// concurrency to conflict.
	//
	// It is a capability rather than a backend name, so a new dialect states
	// what it can do, and nobody adds it to a list somewhere else.
	ConcurrentWriteTransactions bool
}

// Run executes all conformance test groups.
func (s *Suite) Run(t *testing.T) {
	t.Run("users", s.testUsers)
	t.Run("user_prefs", s.testUserPrefs)
	t.Run("sessions", s.testSessions)
	t.Run("session_elevation", s.testSessionElevation)
	t.Run("api_token_elevation", s.testAPITokenElevation)
	t.Run("zero id mutations refused", s.testZeroIDMutationsRefused)
	t.Run("workers", s.testWorkers)
	t.Run("worker_notifications", s.testWorkerNotifications)
	t.Run("registrations", s.testRegistrations)
	t.Run("workspaces", s.testWorkspaces)
	t.Run("workspace_tab_index", s.testWorkspaceTabIndex)
	t.Run("user_op_batches", s.testUserOpBatches)
	// Note: workspace_tabs / workspace_layouts substores were removed
	// during the CRDT migration. Their replacements (WorkspaceTabIndex
	// — bulk read paths covered above; UserOpBatches has a regression
	// case for the empty-journal load path that exercised a SQLite
	// sqlc codegen bug; UserState / UserRecentBatchIDs / LifecycleOutbox)
	// are otherwise exercised via the manager-integration suite rather
	// than via plain table CRUD.
	t.Run("workspace_sections", s.testWorkspaceSections)
	t.Run("workspace_section_items", s.testWorkspaceSectionItems)
	t.Run("oauth_providers", s.testOAuthProviders)
	t.Run("oauth_states", s.testOAuthStates)
	t.Run("oauth_tokens", s.testOAuthTokens)
	t.Run("oauth_user_links", s.testOAuthUserLinks)
	t.Run("pending_oauth_signups", s.testPendingOAuthSignups)
	t.Run("passkeys", s.testPasskeys)
	t.Run("password reset store", s.testPasswordResetStore)
	t.Run("hub_settings", s.testHubSettings)
	t.Run("cli_authorizations", s.testCLIAuthorizations)
	t.Run("transactions", s.testTransactions)
	t.Run("cleanup", s.testCleanup)
	t.Run("cleanup_boundaries", s.testCleanupBoundaries)
	t.Run("time_floor", s.testTimeFloor)
	t.Run("oauth_clients", s.testOAuthClients)
	t.Run("credential_roundtrip", s.testCredentialRoundTrip)
	t.Run("token_revocation", s.testTokenRevocation)
	t.Run("token_listing", s.testTokenListing)
	// `migrator` runs last because its `migrate to zero` subtest leaves
	// the schema partially dropped, and the suite's per-test re-migrate
	// trampoline can't always recover the dropped state cleanly. Any
	// new suite group must therefore land before this line.
	t.Run("migrator", s.testMigrator)
}
