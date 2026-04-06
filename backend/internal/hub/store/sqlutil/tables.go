package sqlutil

import (
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// SQLTruncateTableOrder is the ordered list of SQL tables for truncation.
// Tables are ordered so that foreign key constraints are satisfied
// (children before parents).
var SQLTruncateTableOrder = []string{
	"pending_oauth_signups", "oauth_states", "oauth_tokens", "oauth_user_links", "oauth_providers",
	"workspace_layouts", "workspace_tabs", "workspace_access", "workspace_section_items", "workspace_sections",
	"workspaces", "worker_access_grants", "worker_notifications", "worker_registrations", "workers",
	"user_sessions", "org_members", "users", "orgs",
}

// validEntities is derived from SQLTruncateTableOrder so the allowlist
// stays in sync automatically. It prevents SQL injection from a mistyped
// or malicious entity string in test helpers.
var validEntities = func() map[string]bool {
	m := make(map[string]bool, len(SQLTruncateTableOrder))
	for _, t := range SQLTruncateTableOrder {
		m[t] = true
	}
	return m
}()

// ValidateEntity returns an error if entity is not a known table name.
func ValidateEntity(entity store.TestEntity) error {
	if !validEntities[string(entity)] {
		return fmt.Errorf("unknown entity %q", entity)
	}
	return nil
}
