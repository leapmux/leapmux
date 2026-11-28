package sqlutil

import (
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// KeyColumnKind identifies the Go scalar type scanned for a primary-key or
// AAD-key column during key-version maintenance.
type KeyColumnKind uint8

const (
	KeyKindString KeyColumnKind = iota + 1
	KeyKindInt64
	KeyKindInt32
)

// KeyColumnSpec identifies one primary-key or AAD-key column in an
// EncryptedTableSpec.
type KeyColumnSpec struct {
	Name string
	Kind KeyColumnKind
}

// EncryptedColumnSpec describes one encrypted binary column and the function
// that constructs its canonical AAD from the table's scanned AADColumns values.
type EncryptedColumnSpec struct {
	Name     string
	Nullable bool
	AADFunc  func(aadKeys []any) []byte
}

// EncryptedTableSpec describes a table containing encrypted-at-rest columns.
// Table and column names in this file stay separate from SQL statement
// templates in key_maintenance.go so static owner-filter audits inspect them
// cleanly.
type EncryptedTableSpec struct {
	Table      string
	Label      string
	PrimaryKey []KeyColumnSpec
	AADColumns []KeyColumnSpec
	Columns    []EncryptedColumnSpec
}

// PersistentEncryptedTables lists the 16 persistent tables (35 columns) whose
// rows survive restarts and are covered by CountKeyVersionRefs and ReencryptAll.
var PersistentEncryptedTables = []EncryptedTableSpec{
	{
		Table:      "layout_event_heads",
		Label:      "layout event heads",
		PrimaryKey: []KeyColumnSpec{{Name: "user_id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "user_id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "cursor_key", AADFunc: func(k []any) []byte { return LayoutEventHeadCursorKeyAAD(k[0].(string)) }},
		},
	},
	{
		Table: "layout_event_entries",
		Label: "layout event entries",
		PrimaryKey: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "sequence", Kind: KeyKindInt64},
		},
		AADColumns: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "sequence", Kind: KeyKindInt64},
		},
		Columns: []EncryptedColumnSpec{
			{Name: "fact", AADFunc: func(k []any) []byte { return LayoutEventEntryFactAAD(k[0].(string), k[1].(int64)) }},
		},
	},
	{
		Table:      "users",
		Label:      "users",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "password_hash", AADFunc: func(k []any) []byte { return UserPasswordHashAAD(k[0].(string)) }},
			{Name: "prefs", Nullable: true, AADFunc: func(k []any) []byte { return UserPrefsAAD(k[0].(string)) }},
			{Name: "pending_email_token", Nullable: true, AADFunc: func(k []any) []byte { return UserPendingEmailTokenAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "user_sessions",
		Label:      "user sessions",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "user_agent", AADFunc: func(k []any) []byte { return UserSessionUserAgentAAD(k[0].(string)) }},
			{Name: "ip_address", AADFunc: func(k []any) []byte { return UserSessionIPAddressAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "workspaces",
		Label:      "workspaces",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "title", AADFunc: func(k []any) []byte { return WorkspaceTitleAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "workspace_sections",
		Label:      "workspace sections",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "name", AADFunc: func(k []any) []byte { return WorkspaceSectionNameAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "user_state",
		Label:      "user state",
		PrimaryKey: []KeyColumnSpec{{Name: "user_id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "user_id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "state_payload", AADFunc: func(k []any) []byte { return UserStatePayloadAAD(k[0].(string)) }},
		},
	},
	{
		Table: "user_op_batches",
		Label: "user op batches",
		PrimaryKey: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "physical_ms", Kind: KeyKindInt64},
			{Name: "logical", Kind: KeyKindInt64},
		},
		AADColumns: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "physical_ms", Kind: KeyKindInt64},
			{Name: "logical", Kind: KeyKindInt64},
		},
		Columns: []EncryptedColumnSpec{
			{Name: "batch_payload", AADFunc: func(k []any) []byte { return UserOpBatchPayloadAAD(k[0].(string), k[1].(int64), k[2].(int64)) }},
			{Name: "transitions_payload", AADFunc: func(k []any) []byte {
				return UserOpBatchTransitionsAAD(k[0].(string), k[1].(int64), k[2].(int64))
			}},
		},
	},
	{
		Table:      "lifecycle_outbox",
		Label:      "lifecycle outbox",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindInt64}},
		AADColumns: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "op_type", Kind: KeyKindInt32},
		},
		Columns: []EncryptedColumnSpec{
			{Name: "payload", AADFunc: func(k []any) []byte { return LifecycleOutboxPayloadAAD(k[0].(string), k[1].(int32)) }},
		},
	},
	{
		Table: "worker_tab_publications",
		Label: "worker tab publications",
		PrimaryKey: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "worker_id", Kind: KeyKindString},
			{Name: "tab_id", Kind: KeyKindString},
		},
		AADColumns: []KeyColumnSpec{
			{Name: "user_id", Kind: KeyKindString},
			{Name: "worker_id", Kind: KeyKindString},
			{Name: "tab_id", Kind: KeyKindString},
		},
		Columns: []EncryptedColumnSpec{
			{Name: "response_payload", AADFunc: func(k []any) []byte {
				return WorkerTabPublicationResponseAAD(k[0].(string), k[1].(string), k[2].(string))
			}},
		},
	},
	{
		Table:      "oauth_clients",
		Label:      "OAuth clients",
		PrimaryKey: []KeyColumnSpec{{Name: "client_id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "client_id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "client_name", AADFunc: func(k []any) []byte { return OAuthClientNameAAD(k[0].(string)) }},
			{Name: "icon_blob", Nullable: true, AADFunc: func(k []any) []byte { return OAuthClientIconBlobAAD(k[0].(string)) }},
			{Name: "icon_media_type", Nullable: true, AADFunc: func(k []any) []byte { return OAuthClientIconMediaTypeAAD(k[0].(string)) }},
			{Name: "client_uri", AADFunc: func(k []any) []byte { return OAuthClientURIAAD(k[0].(string)) }},
			{Name: "redirect_uris", AADFunc: func(k []any) []byte { return OAuthClientRedirectURIsAAD(k[0].(string)) }},
			{Name: "scopes", AADFunc: func(k []any) []byte { return OAuthClientScopesAAD(k[0].(string)) }},
			{Name: "grant_types", AADFunc: func(k []any) []byte { return OAuthClientGrantTypesAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "api_tokens",
		Label:      "API tokens",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "installation_name", AADFunc: func(k []any) []byte { return APITokenInstallationNameAAD(k[0].(string)) }},
			{Name: "granted_scopes", AADFunc: func(k []any) []byte { return APITokenGrantedScopesAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "delegation_tokens",
		Label:      "delegation tokens",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "granted_scopes", AADFunc: func(k []any) []byte { return DelegationTokenGrantedScopesAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "oauth_providers",
		Label:      "OAuth providers",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "name", AADFunc: func(k []any) []byte { return OAuthProviderNameAAD(k[0].(string)) }},
			{Name: "issuer_url", AADFunc: func(k []any) []byte { return OAuthProviderIssuerURLAAD(k[0].(string)) }},
			{Name: "client_id", AADFunc: func(k []any) []byte { return OAuthProviderClientIDAAD(k[0].(string)) }},
			{Name: "client_secret", AADFunc: func(k []any) []byte { return OAuthProviderClientSecretAAD(k[0].(string)) }},
			{Name: "scopes", AADFunc: func(k []any) []byte { return OAuthProviderScopesAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "passkey_credentials",
		Label:      "passkey credentials",
		PrimaryKey: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "id", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "public_key", AADFunc: func(k []any) []byte { return PasskeyPublicKeyAAD(k[0].(string)) }},
			{Name: "aaguid", Nullable: true, AADFunc: func(k []any) []byte { return PasskeyAAGUIDAAD(k[0].(string)) }},
			{Name: "transports", AADFunc: func(k []any) []byte { return PasskeyTransportsAAD(k[0].(string)) }},
			{Name: "friendly_name", AADFunc: func(k []any) []byte { return PasskeyFriendlyNameAAD(k[0].(string)) }},
		},
	},
	{
		Table:      "hub_settings",
		Label:      "hub settings",
		PrimaryKey: []KeyColumnSpec{{Name: "key", Kind: KeyKindString}},
		AADColumns: []KeyColumnSpec{{Name: "key", Kind: KeyKindString}},
		Columns: []EncryptedColumnSpec{
			{Name: "value", Nullable: true, AADFunc: func(k []any) []byte { return SettingValueAAD(k[0].(string)) }},
			{Name: "secret", Nullable: true, AADFunc: func(k []any) []byte { return SettingSecretAAD(k[0].(string)) }},
		},
	},
}

// TransientEncryptedColumnSpec describes one encrypted column in a short-lived
// table that is excluded from CountKeyVersionRefs and ReencryptAll.
type TransientEncryptedColumnSpec struct {
	Name     string
	Nullable bool
}

// TransientEncryptedTableSpec describes a short-lived table containing
// encrypted-at-rest columns. Unlike EncryptedTableSpec, it carries only table
// and column metadata used by registry/sqlc completeness tests.
type TransientEncryptedTableSpec struct {
	Table   string
	Columns []TransientEncryptedColumnSpec
}

// TransientEncryptedTables lists the 1 short-lived auto-expiring table (4
// columns) that is encrypted at rest but excluded from CountKeyVersionRefs and
// ReencryptAll. Together with PersistentEncryptedTables they must match every
// keystore.Ciphertext / NullCiphertext override in all three sqlc.yaml files.
var TransientEncryptedTables = []TransientEncryptedTableSpec{
	{
		Table: "oauth_authorization_codes",
		Columns: []TransientEncryptedColumnSpec{
			{Name: "code_challenge"},
			{Name: "redirect_uri"},
			{Name: "granted_scopes"},
			{Name: "installation_name"},
		},
	},
}

// OpenSettingRow decrypts one hub_settings row's value and secret halves
// independently, recording per-column decryption errors on the returned
// store.SettingRow so settings.Manager can degrade unreadable halves without
// aborting GetAll.
func OpenSettingRow(ks *keystore.Keystore, key string, valCt, secCt keystore.NullCiphertext, updatedAt time.Time) store.SettingRow {
	row := store.SettingRow{
		Key:       key,
		UpdatedAt: updatedAt,
	}
	if valCt.Valid() {
		v, err := ks.OpenOptionalString(valCt, SettingValueAAD(key))
		if err != nil {
			row.ValueDecryptErr = fmt.Errorf("decrypt setting %q value: %w", key, err)
		} else {
			row.Value = v
		}
	}
	if secCt.Valid() {
		s, err := ks.OpenOptionalBytes(secCt, SettingSecretAAD(key))
		if err != nil {
			row.SecretDecryptErr = fmt.Errorf("decrypt setting %q secret: %w", key, err)
		} else {
			row.Secret = s
		}
	}
	return row
}
