package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// Suite is the conformance test suite for the store abstraction layer.
// Each backend provides a NewStore function and calls Suite.Run.
type Suite struct {
	NewStore func(t *testing.T) store.TestableStore
}

// Run executes all conformance test groups.
func (s *Suite) Run(t *testing.T) {
	t.Run("orgs", s.testOrgs)
	t.Run("users", s.testUsers)
	t.Run("sessions", s.testSessions)
	t.Run("workers", s.testWorkers)
	t.Run("worker_notifications", s.testWorkerNotifications)
	t.Run("registrations", s.testRegistrations)
	t.Run("workspaces", s.testWorkspaces)
	t.Run("workspace_tab_index", s.testWorkspaceTabIndex)
	t.Run("org_op_batches", s.testOrgOpBatches)
	// Note: workspace_tabs / workspace_layouts substores were removed
	// during the CRDT migration. Their replacements (WorkspaceTabIndex
	// — bulk read paths covered above; OrgOpBatches has a regression
	// case for the empty-journal load path that exercised a SQLite
	// sqlc codegen bug; OrgState / OrgRecentBatchIDs / LifecycleOutbox)
	// are otherwise exercised via the manager-integration suite rather
	// than via plain table CRUD.
	t.Run("workspace_sections", s.testWorkspaceSections)
	t.Run("workspace_section_items", s.testWorkspaceSectionItems)
	t.Run("oauth_providers", s.testOAuthProviders)
	t.Run("oauth_states", s.testOAuthStates)
	t.Run("oauth_tokens", s.testOAuthTokens)
	t.Run("oauth_user_links", s.testOAuthUserLinks)
	t.Run("pending_oauth_signups", s.testPendingOAuthSignups)
	t.Run("cli_authorizations", s.testCLIAuthorizations)
	t.Run("transactions", s.testTransactions)
	t.Run("cleanup", s.testCleanup)
	t.Run("token_revocation", s.testTokenRevocation)
	t.Run("token_listing", s.testTokenListing)
	// `migrator` runs last because its `migrate to zero` subtest leaves
	// the schema partially dropped, and the suite's per-test re-migrate
	// trampoline can't always recover the dropped state cleanly. Any
	// new suite group must therefore land before this line.
	t.Run("migrator", s.testMigrator)
}
