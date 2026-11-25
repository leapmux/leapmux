package sqlutil

import (
	"encoding/hex"
	"strconv"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

// Persistent table AAD constructors (16 tables, 35 columns).

// LayoutEventHeadCursorKeyAAD returns the AAD for layout_event_heads.cursor_key.
func LayoutEventHeadCursorKeyAAD(userID string) []byte {
	return keystore.ColumnAAD("layout_event_heads", "cursor_key", userID)
}

// LayoutEventEntryFactAAD returns the AAD for layout_event_entries.fact.
func LayoutEventEntryFactAAD(userID string, sequence int64) []byte {
	return keystore.ColumnAAD("layout_event_entries", "fact", userID, strconv.FormatInt(sequence, 10))
}

// UserPasswordHashAAD returns the AAD for users.password_hash.
func UserPasswordHashAAD(userID string) []byte {
	return keystore.ColumnAAD("users", "password_hash", userID)
}

// UserPrefsAAD returns the AAD for users.prefs.
func UserPrefsAAD(userID string) []byte {
	return keystore.ColumnAAD("users", "prefs", userID)
}

// UserPendingEmailTokenAAD returns the AAD for users.pending_email_token.
func UserPendingEmailTokenAAD(userID string) []byte {
	return keystore.ColumnAAD("users", "pending_email_token", userID)
}

// UserSessionUserAgentAAD returns the AAD for user_sessions.user_agent.
func UserSessionUserAgentAAD(sessionID string) []byte {
	return keystore.ColumnAAD("user_sessions", "user_agent", sessionID)
}

// UserSessionIPAddressAAD returns the AAD for user_sessions.ip_address.
func UserSessionIPAddressAAD(sessionID string) []byte {
	return keystore.ColumnAAD("user_sessions", "ip_address", sessionID)
}

// WorkspaceTitleAAD returns the AAD for workspaces.title.
func WorkspaceTitleAAD(workspaceID string) []byte {
	return keystore.ColumnAAD("workspaces", "title", workspaceID)
}

// WorkspaceSectionNameAAD returns the AAD for workspace_sections.name.
func WorkspaceSectionNameAAD(sectionID string) []byte {
	return keystore.ColumnAAD("workspace_sections", "name", sectionID)
}

// UserStatePayloadAAD returns the AAD for user_state.state_payload.
func UserStatePayloadAAD(userID string) []byte {
	return keystore.ColumnAAD("user_state", "state_payload", userID)
}

// UserOpBatchPayloadAAD returns the AAD for user_op_batches.batch_payload.
func UserOpBatchPayloadAAD(userID string, physicalMS, logical int64) []byte {
	return keystore.ColumnAAD(
		"user_op_batches",
		"batch_payload",
		userID,
		strconv.FormatInt(physicalMS, 10),
		strconv.FormatInt(logical, 10),
	)
}

// UserOpBatchTransitionsAAD returns the AAD for user_op_batches.transitions_payload.
func UserOpBatchTransitionsAAD(userID string, physicalMS, logical int64) []byte {
	return keystore.ColumnAAD(
		"user_op_batches",
		"transitions_payload",
		userID,
		strconv.FormatInt(physicalMS, 10),
		strconv.FormatInt(logical, 10),
	)
}

// LifecycleOutboxPayloadAAD returns the AAD for lifecycle_outbox.payload.
func LifecycleOutboxPayloadAAD(userID string, opType int32) []byte {
	return keystore.ColumnAAD("lifecycle_outbox", "payload", userID, strconv.FormatInt(int64(opType), 10))
}

// WorkerTabPublicationResponseAAD returns the AAD for worker_tab_publications.response_payload.
func WorkerTabPublicationResponseAAD(userID, workerID, tabID string) []byte {
	return keystore.ColumnAAD("worker_tab_publications", "response_payload", userID, workerID, tabID)
}

// OAuthClientNameAAD returns the AAD for oauth_clients.client_name.
func OAuthClientNameAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "client_name", clientID)
}

// OAuthClientIconBlobAAD returns the AAD for oauth_clients.icon_blob.
func OAuthClientIconBlobAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "icon_blob", clientID)
}

// OAuthClientIconMediaTypeAAD returns the AAD for oauth_clients.icon_media_type.
func OAuthClientIconMediaTypeAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "icon_media_type", clientID)
}

// OAuthClientURIAAD returns the AAD for oauth_clients.client_uri.
func OAuthClientURIAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "client_uri", clientID)
}

// OAuthClientRedirectURIsAAD returns the AAD for oauth_clients.redirect_uris.
func OAuthClientRedirectURIsAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "redirect_uris", clientID)
}

// OAuthClientScopesAAD returns the AAD for oauth_clients.scopes.
func OAuthClientScopesAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "scopes", clientID)
}

// OAuthClientGrantTypesAAD returns the AAD for oauth_clients.grant_types.
func OAuthClientGrantTypesAAD(clientID string) []byte {
	return keystore.ColumnAAD("oauth_clients", "grant_types", clientID)
}

// APITokenInstallationNameAAD returns the AAD for api_tokens.installation_name.
func APITokenInstallationNameAAD(tokenID string) []byte {
	return keystore.ColumnAAD("api_tokens", "installation_name", tokenID)
}

// APITokenGrantedScopesAAD returns the AAD for api_tokens.granted_scopes.
func APITokenGrantedScopesAAD(tokenID string) []byte {
	return keystore.ColumnAAD("api_tokens", "granted_scopes", tokenID)
}

// DelegationTokenGrantedScopesAAD returns the AAD for delegation_tokens.granted_scopes.
func DelegationTokenGrantedScopesAAD(tokenID string) []byte {
	return keystore.ColumnAAD("delegation_tokens", "granted_scopes", tokenID)
}

// OAuthProviderNameAAD returns the AAD for oauth_providers.name.
func OAuthProviderNameAAD(providerID string) []byte {
	return keystore.ColumnAAD("oauth_providers", "name", providerID)
}

// OAuthProviderIssuerURLAAD returns the AAD for oauth_providers.issuer_url.
func OAuthProviderIssuerURLAAD(providerID string) []byte {
	return keystore.ColumnAAD("oauth_providers", "issuer_url", providerID)
}

// OAuthProviderClientIDAAD returns the AAD for oauth_providers.client_id.
func OAuthProviderClientIDAAD(providerID string) []byte {
	return keystore.ColumnAAD("oauth_providers", "client_id", providerID)
}

// OAuthProviderClientSecretAAD returns the AAD for oauth_providers.client_secret.
func OAuthProviderClientSecretAAD(providerID string) []byte {
	return keystore.ColumnAAD("oauth_providers", "client_secret", providerID)
}

// OAuthProviderScopesAAD returns the AAD for oauth_providers.scopes.
func OAuthProviderScopesAAD(providerID string) []byte {
	return keystore.ColumnAAD("oauth_providers", "scopes", providerID)
}

// PasskeyPublicKeyAAD returns the AAD for passkey_credentials.public_key.
func PasskeyPublicKeyAAD(credentialRowID string) []byte {
	return keystore.ColumnAAD("passkey_credentials", "public_key", credentialRowID)
}

// PasskeyAAGUIDAAD returns the AAD for passkey_credentials.aaguid.
func PasskeyAAGUIDAAD(credentialRowID string) []byte {
	return keystore.ColumnAAD("passkey_credentials", "aaguid", credentialRowID)
}

// PasskeyTransportsAAD returns the AAD for passkey_credentials.transports.
func PasskeyTransportsAAD(credentialRowID string) []byte {
	return keystore.ColumnAAD("passkey_credentials", "transports", credentialRowID)
}

// PasskeyFriendlyNameAAD returns the AAD for passkey_credentials.friendly_name.
func PasskeyFriendlyNameAAD(credentialRowID string) []byte {
	return keystore.ColumnAAD("passkey_credentials", "friendly_name", credentialRowID)
}

// SettingValueAAD returns the AAD for hub_settings.value.
func SettingValueAAD(key string) []byte {
	return keystore.ColumnAAD("hub_settings", "value", key)
}

// SettingSecretAAD returns the AAD for hub_settings.secret.
func SettingSecretAAD(key string) []byte {
	return keystore.ColumnAAD("hub_settings", "secret", key)
}

// Transient table AAD constructors (1 table, 4 columns).

// OAuthAuthCodeChallengeAAD returns the AAD for oauth_authorization_codes.code_challenge.
func OAuthAuthCodeChallengeAAD(codeHash []byte) []byte {
	return keystore.ColumnAAD("oauth_authorization_codes", "code_challenge", hex.EncodeToString(codeHash))
}

// OAuthAuthCodeRedirectURIAAD returns the AAD for oauth_authorization_codes.redirect_uri.
func OAuthAuthCodeRedirectURIAAD(codeHash []byte) []byte {
	return keystore.ColumnAAD("oauth_authorization_codes", "redirect_uri", hex.EncodeToString(codeHash))
}

// OAuthAuthCodeGrantedScopesAAD returns the AAD for oauth_authorization_codes.granted_scopes.
func OAuthAuthCodeGrantedScopesAAD(codeHash []byte) []byte {
	return keystore.ColumnAAD("oauth_authorization_codes", "granted_scopes", hex.EncodeToString(codeHash))
}

// OAuthAuthCodeInstallationNameAAD returns the AAD for oauth_authorization_codes.installation_name.
func OAuthAuthCodeInstallationNameAAD(codeHash []byte) []byte {
	return keystore.ColumnAAD("oauth_authorization_codes", "installation_name", hex.EncodeToString(codeHash))
}
