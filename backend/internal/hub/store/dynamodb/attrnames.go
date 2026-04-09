package dynamodb

// DynamoDB attribute name constants. Using constants instead of raw
// strings prevents silent data bugs from typos — a misspelled constant
// is a compile error, while a misspelled string returns a zero value.

// Common attributes shared across multiple tables.
const (
	attrID        = "id"
	attrUserID    = "user_id"
	attrOrgID     = "org_id"
	attrWorkerID  = "worker_id"
	attrCreatedAt = "created_at"
	attrUpdatedAt = "updated_at"
	attrDeletedAt = "deleted_at"
	attrDeleted   = "deleted"
	attrExpiresAt = "expires_at"
	attrStatus    = "status"
	attrName      = "name"
)

// User attributes.
const (
	attrUsername              = "username"
	attrPasswordHash          = "password_hash"
	attrDisplayName           = "display_name"
	attrEmail                 = "email"
	attrEmailVerified         = "email_verified"
	attrPasswordSet           = "password_set"
	attrIsAdmin               = "is_admin"
	attrPrefs                 = "prefs"
	attrPendingEmail          = "pending_email"
	attrPendingEmailToken     = "pending_email_token"
	attrPendingEmailExpiresAt = "pending_email_expires_at"
)

// Org attributes.
const (
	attrIsPersonal = "is_personal"
)

// Org member attributes.
const (
	attrRole     = "role"
	attrJoinedAt = "joined_at"
)

// Session attributes.
const (
	attrLastActiveAt = "last_active_at"
	attrIPAddress    = "ip_address"
	attrUserAgent    = "user_agent"
	attrNotExpired   = "not_expired"
)

// Worker attributes.
const (
	attrAuthToken       = "auth_token"
	attrRegisteredBy    = "registered_by"
	attrLastSeenAt      = "last_seen_at"
	attrPublicKey       = "public_key"
	attrMlkemPublicKey  = "mlkem_public_key"
	attrSlhdsaPublicKey = "slhdsa_public_key"
)

// Worker notification attributes.
const (
	attrType        = "type"
	attrPayload     = "payload"
	attrMaxAttempts = "max_attempts"
	attrAttempts    = "attempts"
	attrDeliveredAt = "delivered_at"
)

// Worker access grant attributes.
const (
	attrGrantedBy = "granted_by"
)

// Registration attributes.
const (
	attrVersion    = "version"
	attrApprovedBy = "approved_by"
)

// Workspace attributes.
const (
	attrWorkspaceID = "workspace_id"
	attrOwnerUserID = "owner_user_id"
	attrTitle       = "title"
	attrIsDeleted   = "is_deleted"
)

// Workspace tab attributes.
const (
	attrTabTypeSK = "tab_type#tab_id" // composite sort key
	attrTileID    = "tile_id"
	attrPosition  = "position"
)

// Workspace section attributes.
const (
	attrSectionType = "section_type"
	attrSectionID   = "section_id"
	attrSidebar     = "sidebar"
)

// Workspace layout attributes.
const (
	attrLayoutJSON = "layout_json"
)

// OAuth provider attributes.
const (
	attrProviderType = "provider_type"
	attrIssuerURL    = "issuer_url"
	attrClientID     = "client_id"
	attrClientSecret = "client_secret"
	attrScopes       = "scopes"
	attrTrustEmail   = "trust_email"
	attrEnabled      = "enabled"
)

// OAuth state attributes.
const (
	attrProviderID   = "provider_id"
	attrState        = "state"
	attrRedirectURI  = "redirect_uri"
	attrPKCEVerifier = "pkce_verifier"
	attrActive       = "active"
)

// OAuth token attributes.
const (
	attrAccessToken     = "access_token"
	attrRefreshToken    = "refresh_token"
	attrTokenType       = "token_type"
	attrKeyVersion      = "key_version"
	attrExpiryPartition = "expiry_partition"
)

// Pending OAuth signup attributes.
const (
	attrProviderSubject = "provider_subject"
	attrToken           = "token"
	attrTokenExpiresAt  = "token_expires_at"
)

// Unique constraint attributes.
const (
	attrConstraintValue = "constraint_value"
)

// TTL attribute (used for DynamoDB native TTL).
const (
	attrTTL = "ttl"
)

// Migration meta table attributes.
const (
	attrKey   = "key"
	attrValue = "value"
)
