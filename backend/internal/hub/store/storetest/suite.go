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
	t.Run("org_members", s.testOrgMembers)
	t.Run("workers", s.testWorkers)
	t.Run("worker_access_grants", s.testWorkerAccessGrants)
	t.Run("worker_notifications", s.testWorkerNotifications)
	t.Run("registrations", s.testRegistrations)
	t.Run("workspaces", s.testWorkspaces)
	t.Run("workspace_access", s.testWorkspaceAccess)
	t.Run("workspace_tabs", s.testWorkspaceTabs)
	t.Run("workspace_layouts", s.testWorkspaceLayouts)
	t.Run("workspace_sections", s.testWorkspaceSections)
	t.Run("workspace_section_items", s.testWorkspaceSectionItems)
	t.Run("oauth_providers", s.testOAuthProviders)
	t.Run("oauth_states", s.testOAuthStates)
	t.Run("oauth_tokens", s.testOAuthTokens)
	t.Run("oauth_user_links", s.testOAuthUserLinks)
	t.Run("pending_oauth_signups", s.testPendingOAuthSignups)
	t.Run("transactions", s.testTransactions)
	t.Run("cleanup", s.testCleanup)
	t.Run("migrator", s.testMigrator)
}
