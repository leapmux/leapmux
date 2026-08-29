package sqlutil

// SQLTruncateTableOrder is the ordered list of SQL tables for truncation.
// Tables are ordered so that foreign key constraints are satisfied
// (children before parents).
var SQLTruncateTableOrder = []string{
	"oauth_authorization_codes", "device_authorizations",
	"pending_oauth_signups", "oauth_states", "oauth_tokens", "oauth_user_links", "oauth_providers",
	"altcha_used_salts", "hub_settings",
	"hub_runtime_lease", "revocation_events", "revocation_event_sequence",
	"lifecycle_outbox", "user_recent_batch_ids", "workspace_tab_rendered", "workspace_tab_owned",
	"user_state", "user_op_batches",
	"workspace_section_items", "workspace_sections",
	"delegation_tokens", "api_tokens",
	// oauth_clients comes after every table whose foreign key points at it --
	// api_tokens, device_authorizations and oauth_authorization_codes -- and
	// before users, which its own owner and vouch columns point at.
	"oauth_clients",
	"workspaces", "worker_notifications", "worker_registration_keys", "workers",
	"passkey_credentials", "webauthn_sessions",
	"user_sessions", "users",
}

// TruncateStatement is the DELETE one table needs to be emptied between tests.
//
// It exists for the ONE table whose rows are not all test data. Every store
// open seeds oauth_clients with the two registrations this build ships
// (store.SeedBuiltIns) -- their fields are constants of the build, and
// api_tokens.client_id is a NOT NULL foreign key onto them -- so a plain
// DELETE leaves every later insert failing the constraint. It is the same
// reason the helpers re-seed revocation_event_sequence, expressed as a
// predicate rather than as a re-insert so no second copy of the seeded VALUES
// exists.
//
// A statement rather than a second list, because a caller that iterated one
// list for the plain tables and another for the predicated ones could drop a
// table from both.
func TruncateStatement(table string) string {
	if table == "oauth_clients" {
		// The built-in registrations are schema, not fixture data.
		return "DELETE FROM oauth_clients WHERE registration_source <> 'builtin'"
	}
	return "DELETE FROM " + table
}
