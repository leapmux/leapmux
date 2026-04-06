package dynamodb

// GSI (Global Secondary Index) name constants for all DynamoDB tables.
// Centralising these prevents typos and makes renames a single-constant change.
const (
	// Shared across orgs, users, workers — partition on soft-delete flag.
	gsiDeletedCreatedAt = "deleted-created_at-index"
	gsiDeletedDeletedAt = "deleted-deleted_at-index"

	// Sessions — partition on the "not_expired" sentinel.
	gsiNotExpiredExpiresAt    = "not_expired-expires_at-index"
	gsiNotExpiredLastActiveAt = "not_expired-last_active_at-index"

	// Shared user_id GSI used by sessions, org_members, workers,
	// worker_access_grants, workspace_access, workspace_sections,
	// workspace_section_items, workspaces.
	gsiUserID = "user_id-index"

	// Orgs
	gsiOrgName = "name-index"

	// Users
	gsiUsername          = "username-index"
	gsiEmail             = "email-index"
	gsiOrgID             = "org_id-index"
	gsiPendingEmail      = "pending_email-index"
	gsiPendingEmailToken = "pending_email_token-index"

	// Workers
	gsiAuthToken    = "auth_token-index"
	gsiRegisteredBy = "registered_by-index"

	// Worker notifications
	gsiWorkerIDStatus = "worker_id-status-index"

	// Registrations
	gsiStatus = "status-index"

	// OAuth tokens
	gsiExpiry     = "expiry-index"
	gsiKeyVersion = "key_version-index"
	gsiProviderID = "provider_id-index"

	// OAuth user links
	gsiProviderSubject = "provider_subject-index"

	// Workspace tabs
	gsiWorkerID = "worker_id-index"

	// OAuth states — sentinel for cleanup queries.
	gsiActiveExpiresAt = "active-expires_at-index"

	// Workspace section items
	gsiSectionID   = "section_id-index"
	gsiWorkspaceID = "workspace_id-index"

	// Workspaces
	gsiOrgOwner    = "org_owner-index"
	gsiOwnerUserID = "owner_user_id-index"
)
