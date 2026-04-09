package sqlutil

// SQLTruncateTableOrder is the ordered list of SQL tables for truncation.
// Tables are ordered so that foreign key constraints are satisfied
// (children before parents).
var SQLTruncateTableOrder = []string{
	"pending_oauth_signups", "oauth_states", "oauth_tokens", "oauth_user_links", "oauth_providers",
	"workspace_layouts", "workspace_tabs", "workspace_access", "workspace_section_items", "workspace_sections",
	"workspaces", "worker_access_grants", "worker_notifications", "worker_registrations", "workers",
	"user_sessions", "org_members", "users", "orgs",
}
