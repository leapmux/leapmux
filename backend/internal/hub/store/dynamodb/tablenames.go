package dynamodb

// Table name constants for all DynamoDB tables (without prefix).
const (
	tableOrgs                  = "orgs"
	tableUsers                 = "users"
	tableSessions              = "user_sessions"
	tableOrgMembers            = "org_members"
	tableWorkers               = "workers"
	tableWorkerGrants          = "worker_access_grants"
	tableWorkerNotifications   = "worker_notifications"
	tableRegistrations         = "worker_registrations"
	tableWorkspaces            = "workspaces"
	tableWorkspaceAccess       = "workspace_access"
	tableWorkspaceTabs         = "workspace_tabs"
	tableWorkspaceLayouts      = "workspace_layouts"
	tableWorkspaceSections     = "workspace_sections"
	tableWorkspaceSectionItems = "workspace_section_items"
	tableOAuthProviders        = "oauth_providers"
	tableOAuthStates           = "oauth_states"
	tableOAuthTokens           = "oauth_tokens"
	tableOAuthUserLinks        = "oauth_user_links"
	tablePendingOAuthSignups   = "pending_oauth_signups"
	tableUniqueConstraints     = "unique_constraints"
	tableMeta                  = "meta"

	// metaKeySchemaVersion is the key used in the meta table to store
	// the current schema version.
	metaKeySchemaVersion = "schema_version"
)
