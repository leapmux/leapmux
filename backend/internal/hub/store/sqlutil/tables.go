package sqlutil

// SQLTruncateTableOrder is the ordered list of SQL tables for truncation.
// Tables are ordered so that foreign key constraints are satisfied
// (children before parents).
var SQLTruncateTableOrder = []string{
	"pending_oauth_signups", "oauth_states", "oauth_tokens", "oauth_user_links", "oauth_providers",
	"hub_runtime_lease", "revocation_events", "revocation_event_sequence",
	"lifecycle_outbox", "org_recent_batch_ids", "workspace_tab_rendered", "workspace_tab_owned",
	"org_state", "org_op_batches",
	"workspace_access", "workspace_section_items", "workspace_sections",
	"delegation_tokens", "api_tokens",
	"workspaces", "worker_access_grants", "worker_notifications", "worker_registration_keys", "workers",
	"user_sessions", "org_members", "users", "orgs",
}
