package storetest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func setupRotationKeystores(t *testing.T) (ksV1, ksV12, ksV2 *keystore.Keystore) {
	t.Helper()
	k1, err := keystore.GenerateKey()
	require.NoError(t, err)
	k2, err := keystore.GenerateKey()
	require.NoError(t, err)
	ksV1, err = keystore.New(map[uint32][32]byte{1: k1})
	require.NoError(t, err)
	ksV12, err = keystore.New(map[uint32][32]byte{1: k1, 2: k2})
	require.NoError(t, err)
	ksV2, err = keystore.New(map[uint32][32]byte{2: k2})
	require.NoError(t, err)
	return ksV1, ksV12, ksV2
}

// useSwappedKeystore returns a borrowed store handle using swappedKS, seeds
// built-in oauth_clients under swappedKS, and registers cleanup to clear all
// rows encrypted under swappedKS and restore built-ins under st's own keystore
// before the next subtest's Migrator().Migrate runs VerifyKeyring.
func useSwappedKeystore(t *testing.T, ctx context.Context, st store.TestableStore, swappedKS *keystore.Keystore, now time.Time) store.TestableStore {
	t.Helper()
	swapped := st.TestHelper().WithKeystore(swappedKS)
	require.NoError(t, store.SeedBuiltIns(ctx, swapped, now))
	t.Cleanup(func() {
		_ = st.TestHelper().TruncateAll(context.Background())
		_ = store.SeedBuiltIns(context.Background(), st, time.Now().UTC())
	})
	return swapped
}

func (s *Suite) testEncryptionAtRest(t *testing.T) {
	t.Run("no plaintext on disk across all 15 tables", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)

		type needleCheck struct {
			table  string
			column string
			needle string
		}
		var checks []needleCheck
		addNeedle := func(table, column, needle string) string {
			checks = append(checks, needleCheck{table: table, column: column, needle: needle})
			return needle
		}

		// 1. users (3 encrypted columns: password_hash, prefs, pending_email_token)
		userID := id.Generate()
		uid := userid.MustNew(userID)
		pwNeedle := addNeedle("users", "password_hash", "needle-01-users-password-hash")
		prefsNeedle := addNeedle("users", "prefs", `{"theme":"needle-02-users-prefs"}`)
		pendingTokenNeedle := addNeedle("users", "pending_email_token", "needle-03-users-pending-email-token")

		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID:                    userID,
			Username:              "encuser",
			PasswordHash:          pwNeedle,
			DisplayName:           "Encryption User",
			Email:                 "encuser@example.com",
			EmailVerified:         true,
			FirstCredentialExempt: true,
			IsAdmin:               true,
		}))
		// Initially prefs is NULL (GetPrefs returns default "{}") and pending_email_token is NULL.
		gotPrefsEmpty, err := st.Users().GetPrefs(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "{}", gotPrefsEmpty)

		require.NoError(t, st.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
			ID:    userID,
			Prefs: prefsNeedle,
		}))
		pendingExp := now.Add(time.Hour)
		minted, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      userID,
			PendingEmail:            "pending-enc@example.com",
			PendingEmailToken:       pendingTokenNeedle,
			PendingEmailExpiresAt:   &pendingExp,
			PendingEmailUnblockedAt: now.Add(time.Minute),
			Now:                     now,
		})
		mustSetPendingEmail(t, minted, err)

		gotUser, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, pwNeedle, gotUser.PasswordHash)
		assert.Equal(t, prefsNeedle, gotUser.Prefs)
		assert.Equal(t, pendingTokenNeedle, gotUser.PendingEmailToken)

		// Edge case: SetPendingEmail with empty PendingEmailToken "" roundtrips as "".
		emptyTokenUser := SeedUser(t, st, "empty-pending-token-user")
		minted, err = st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      emptyTokenUser.ID,
			PendingEmail:            "empty-token@example.com",
			PendingEmailToken:       "",
			PendingEmailExpiresAt:   &pendingExp,
			PendingEmailUnblockedAt: now.Add(time.Minute),
			Now:                     now,
		})
		mustSetPendingEmail(t, minted, err)
		gotEmptyTokenUser, err := st.Users().GetByID(ctx, emptyTokenUser.ID)
		require.NoError(t, err)
		assert.Empty(t, gotEmptyTokenUser.PendingEmailToken)

		// 2. user_sessions (2 encrypted columns: user_agent, ip_address)
		sessID := id.Generate()
		uaNeedle := addNeedle("user_sessions", "user_agent", "needle-04-user-sessions-user-agent")
		ipNeedle := addNeedle("user_sessions", "ip_address", "needle-05-user-sessions-ip-address")
		require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
			SecretHash: FixtureSecretHash("enc-session-secret"),
			ID:         sessID,
			UserID:     uid,
			ExpiresAt:  now.Add(24 * time.Hour),
			UserAgent:  uaNeedle,
			IPAddress:  ipNeedle,
		}))
		gotSess, err := st.Sessions().GetByID(ctx, sessID, now)
		require.NoError(t, err)
		assert.Equal(t, uaNeedle, gotSess.UserAgent)
		assert.Equal(t, ipNeedle, gotSess.IPAddress)

		// Edge case: empty strings in user_sessions.
		emptySessID := id.Generate()
		require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
			SecretHash: FixtureSecretHash("enc-empty-session-secret"),
			ID:         emptySessID,
			UserID:     uid,
			ExpiresAt:  now.Add(24 * time.Hour),
			UserAgent:  "",
			IPAddress:  "",
		}))
		gotEmptySess, err := st.Sessions().GetByID(ctx, emptySessID, now)
		require.NoError(t, err)
		assert.Empty(t, gotEmptySess.UserAgent)
		assert.Empty(t, gotEmptySess.IPAddress)

		// 3. workspaces (1 encrypted column: title)
		wsID := id.Generate()
		wsTitleNeedle := addNeedle("workspaces", "title", "needle-06-workspaces-title")
		require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID:          wsID,
			OwnerUserID: uid,
			Title:       wsTitleNeedle,
		}))
		gotWS, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsTitleNeedle, gotWS.Title)

		// Edge case: empty workspace title "".
		emptyWSID := id.Generate()
		require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID:          emptyWSID,
			OwnerUserID: uid,
			Title:       "",
		}))
		gotEmptyWS, err := st.Workspaces().GetByID(ctx, emptyWSID)
		require.NoError(t, err)
		assert.Empty(t, gotEmptyWS.Title)

		// 4. workspace_sections (1 encrypted column: name)
		secID := id.Generate()
		secNameNeedle := addNeedle("workspace_sections", "name", "needle-07-workspace-sections-name")
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      uid,
			Name:        secNameNeedle,
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		gotSec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, secNameNeedle, gotSec.Name)

		// 5. user_state (1 encrypted column: state_payload)
		stateNeedle := addNeedle("user_state", "state_payload", "needle-08-user-state-payload")
		stateParams, err := store.NewUpsertUserStateParams(&leapmuxv1.UserCrdtState{
			UserId: userID,
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
				stateNeedle: {},
			},
		})
		require.NoError(t, err)
		require.NoError(t, st.UserState().Upsert(ctx, stateParams))
		gotState, err := st.UserState().Get(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, stateParams.StatePayload(), gotState.StatePayload)

		// 6. user_op_batches (2 encrypted columns: batch_payload, transitions_payload)
		batchNeedle := addNeedle("user_op_batches", "batch_payload", "needle-09-user-op-batches-batch-payload")
		transNeedle := addNeedle("user_op_batches", "transitions_payload", "needle-10-user-op-batches-transitions-payload")
		require.NoError(t, st.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID:             uid,
			PhysicalMs:         1000,
			Logical:            1,
			BatchID:            "batch-enc-1",
			BatchPayload:       []byte(batchNeedle),
			TransitionsPayload: []byte(transNeedle),
			OpCount:            1,
		}))
		gotBatches, err := st.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID:          uid,
			AfterPhysicalMs: 0,
			AfterLogical:    0,
			Limit:           10,
		})
		require.NoError(t, err)
		require.Len(t, gotBatches, 1)
		assert.Equal(t, []byte(batchNeedle), gotBatches[0].BatchPayload)
		assert.Equal(t, []byte(transNeedle), gotBatches[0].TransitionsPayload)

		// 7. lifecycle_outbox (1 encrypted column: payload)
		outboxNeedle := addNeedle("lifecycle_outbox", "payload", "needle-11-lifecycle-outbox-payload")
		require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
			UserID:  uid,
			OpType:  leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE,
			Payload: []byte(outboxNeedle),
		}))
		gotOutbox, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
			UserID: uid,
			Limit:  10,
		})
		require.NoError(t, err)
		require.Len(t, gotOutbox, 1)
		assert.Equal(t, []byte(outboxNeedle), gotOutbox[0].Payload)

		// 8. worker_tab_publications (1 encrypted column: response_payload)
		worker := SeedWorker(t, st, userID)
		pubNeedle := addNeedle("worker_tab_publications", "response_payload", "needle-12-worker-tab-publications-response")
		pubKey := store.WorkerTabPublicationKey{
			UserID:   uid,
			WorkerID: worker.ID,
			TabID:    "tab-enc-1",
		}
		require.NoError(t, st.WorkerTabPublications().Insert(ctx, store.InsertWorkerTabPublicationParams{
			WorkerTabPublicationKey: pubKey,
			WorkerTabPublication: store.WorkerTabPublication{
				RequestHash:     FixtureSecretHash("pub-req-hash"),
				ResponsePayload: []byte(pubNeedle),
			},
		}))
		gotPub, err := st.WorkerTabPublications().Get(ctx, pubKey)
		require.NoError(t, err)
		assert.Equal(t, []byte(pubNeedle), gotPub.ResponsePayload)

		// 9. oauth_clients (7 encrypted columns: client_name, icon_blob, icon_media_type,
		//    client_uri, redirect_uris, scopes, grant_types)
		// Includes max-size 64 KiB icon_blob regression test (MySQL MEDIUMBLOB).
		clientID := "app-enc-" + id.Generate()
		clientNameNeedle := addNeedle("oauth_clients", "client_name", "needle-13-oauth-clients-name")
		iconBlobNeedle := addNeedle("oauth_clients", "icon_blob", "needle-14-oauth-clients-icon-blob")
		iconMediaNeedle := addNeedle("oauth_clients", "icon_media_type", "needle-15-oauth-clients-icon-media")
		clientURINeedle := addNeedle("oauth_clients", "client_uri", "https://needle-16-oauth-clients-uri.example.com")
		redirectURIsNeedle := addNeedle("oauth_clients", "redirect_uris", "https://needle-17-oauth-clients-redirect.example.com/cb")
		clientScopesNeedle := addNeedle("oauth_clients", "scopes", "needle-18-oauth-clients-scopes")
		grantTypesNeedle := addNeedle("oauth_clients", "grant_types", "needle-19-oauth-clients-grant-types")

		icon64KiB := bytes.Repeat([]byte("I"), 64*1024)
		copy(icon64KiB, []byte(iconBlobNeedle))
		createdClient, err := st.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
			ClientID:           clientID,
			OwnerUserID:        userID,
			ClientName:         clientNameNeedle,
			IconBlob:           icon64KiB,
			IconMediaType:      iconMediaNeedle,
			ClientURI:          clientURINeedle,
			RedirectURIs:       redirectURIsNeedle,
			Scopes:             clientScopesNeedle,
			GrantTypes:         grantTypesNeedle,
			ElevationAllowed:   true,
			RegistrationSource: store.OAuthClientSourceUser,
		})
		require.NoError(t, err)
		assert.True(t, createdClient.HasIcon)
		assert.Equal(t, clientNameNeedle, createdClient.ClientName)
		assert.Equal(t, clientURINeedle, createdClient.ClientURI)
		assert.Equal(t, redirectURIsNeedle, createdClient.RedirectURIs)
		assert.Equal(t, clientScopesNeedle, createdClient.Scopes)
		assert.Equal(t, grantTypesNeedle, createdClient.GrantTypes)

		gotIcon, err := st.OAuthClients().GetIcon(ctx, clientID)
		require.NoError(t, err)
		assert.Equal(t, icon64KiB, gotIcon.IconBlob)
		assert.Equal(t, iconMediaNeedle, gotIcon.IconMediaType)

		// Edge case: oauth_clients.Create and SetIcon with empty []byte{} stores NULL so HasIcon == false.
		noIconClientID := "app-noicon-" + id.Generate()
		noIconClient, err := st.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
			ClientID:           noIconClientID,
			OwnerUserID:        userID,
			ClientName:         "No Icon App",
			IconBlob:           []byte{},
			IconMediaType:      "",
			ClientURI:          "",
			RedirectURIs:       "http://127.0.0.1/cb",
			Scopes:             "read",
			GrantTypes:         "authorization_code",
			RegistrationSource: store.OAuthClientSourceUser,
		})
		require.NoError(t, err)
		assert.False(t, noIconClient.HasIcon, "Create with empty IconBlob must report HasIcon == false")
		rows, err := st.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
			ClientID:      noIconClientID,
			IconBlob:      []byte("png-bytes"),
			IconMediaType: "image/png",
			CallerUserID:  uid,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
		gotWithIcon, err := st.OAuthClients().Get(ctx, noIconClientID)
		require.NoError(t, err)
		assert.True(t, gotWithIcon.HasIcon)
		rows, err = st.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
			ClientID:      noIconClientID,
			IconBlob:      []byte{},
			IconMediaType: "",
			CallerUserID:  uid,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
		gotClearedIcon, err := st.OAuthClients().Get(ctx, noIconClientID)
		require.NoError(t, err)
		assert.False(t, gotClearedIcon.HasIcon, "SetIcon with empty IconBlob must clear icon and report HasIcon == false")

		// 10. api_tokens (2 encrypted columns: installation_name, granted_scopes)
		apiTokID := id.Generate()
		apiInstNeedle := addNeedle("api_tokens", "installation_name", "needle-20-api-tokens-installation-name")
		apiScopesNeedle := addNeedle("api_tokens", "granted_scopes", "needle-21-api-tokens-granted-scopes")
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               apiTokID,
			UserID:           uid,
			ClientID:         clientID,
			InstallationName: apiInstNeedle,
			GrantedScopes:    apiScopesNeedle,
			Issuance:         store.APITokenIssuanceAuthorizationCode,
			SecretHash:       FixtureSecretHash("api-tok-secret"),
		}))
		gotAPITok, err := st.APITokens().GetByID(ctx, apiTokID)
		require.NoError(t, err)
		assert.Equal(t, apiInstNeedle, gotAPITok.InstallationName)
		assert.Equal(t, apiScopesNeedle, gotAPITok.GrantedScopes)

		// 11. delegation_tokens (1 encrypted column: granted_scopes)
		delTokID := id.Generate()
		delScopesNeedle := addNeedle("delegation_tokens", "granted_scopes", "needle-22-delegation-tokens-granted-scopes")
		require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			ID:            delTokID,
			UserID:        uid,
			WorkerID:      worker.ID,
			GrantedScopes: delScopesNeedle,
			SecretHash:    FixtureSecretHash("del-tok-secret"),
			ExpiresAt:     now.Add(time.Hour),
		}))
		gotDelTok, err := st.DelegationTokens().GetByID(ctx, delTokID)
		require.NoError(t, err)
		assert.Equal(t, delScopesNeedle, gotDelTok.GrantedScopes)

		// 12. oauth_providers (5 encrypted columns: name, issuer_url, client_id, client_secret, scopes)
		provID := id.Generate()
		provNameNeedle := addNeedle("oauth_providers", "name", "needle-23-oauth-providers-name")
		provIssuerNeedle := addNeedle("oauth_providers", "issuer_url", "https://needle-24-oauth-providers-issuer.example.com")
		provClientIDNeedle := addNeedle("oauth_providers", "client_id", "needle-25-oauth-providers-client-id")
		provSecretNeedle := addNeedle("oauth_providers", "client_secret", "needle-26-oauth-providers-client-secret")
		provScopesNeedle := addNeedle("oauth_providers", "scopes", "needle-27-oauth-providers-scopes")
		require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID:           provID,
			ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
			Name:         provNameNeedle,
			IssuerURL:    provIssuerNeedle,
			ClientID:     provClientIDNeedle,
			ClientSecret: []byte(provSecretNeedle),
			Scopes:       provScopesNeedle,
			TrustEmail:   true,
			Enabled:      true,
		}))
		gotProv, err := st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.Equal(t, provNameNeedle, gotProv.Name)
		assert.Equal(t, provIssuerNeedle, gotProv.IssuerURL)
		assert.Equal(t, provClientIDNeedle, gotProv.ClientID)
		assert.Equal(t, []byte(provSecretNeedle), gotProv.ClientSecret)
		assert.Equal(t, provScopesNeedle, gotProv.Scopes)

		// 13. passkey_credentials (4 encrypted columns: public_key, aaguid, transports, friendly_name)
		pkID := id.Generate()
		pkPubNeedle := addNeedle("passkey_credentials", "public_key", "needle-31-passkey-public-key")
		pkAAGUIDNeedle := addNeedle("passkey_credentials", "aaguid", "needle-32-passkey-aaguid")
		pkTransportsNeedle := addNeedle("passkey_credentials", "transports", "needle-33-passkey-transports")
		pkNameNeedle := addNeedle("passkey_credentials", "friendly_name", "needle-34-passkey-friendly-name")
		require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           pkID,
			UserID:       userID,
			CredentialID: []byte("cred-id-enc-1"),
			PublicKey:    []byte(pkPubNeedle),
			SignCount:    1,
			AAGUID:       []byte(pkAAGUIDNeedle),
			Transports:   pkTransportsNeedle,
			FriendlyName: pkNameNeedle,
			CreatedAt:    now,
		}))
		gotPK, err := st.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.Equal(t, []byte(pkPubNeedle), gotPK.PublicKey)
		assert.Equal(t, []byte(pkAAGUIDNeedle), gotPK.AAGUID)
		assert.Equal(t, pkTransportsNeedle, gotPK.Transports)
		assert.Equal(t, pkNameNeedle, gotPK.FriendlyName)

		// Edge case: passkey with NULL AAGUID.
		pkNullID := id.Generate()
		require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           pkNullID,
			UserID:       userID,
			CredentialID: []byte("cred-id-enc-null"),
			PublicKey:    []byte("pk-null-aaguid"),
			AAGUID:       nil,
			Transports:   "",
			FriendlyName: "",
			CreatedAt:    now,
		}))
		gotPKNull, err := st.PasskeyCredentials().GetByID(ctx, pkNullID)
		require.NoError(t, err)
		assert.Nil(t, gotPKNull.AAGUID)
		assert.Empty(t, gotPKNull.Transports)
		assert.Empty(t, gotPKNull.FriendlyName)

		// 14. hub_settings (2 encrypted columns: value, secret)
		setValNeedle := addNeedle("hub_settings", "value", `{"v":"needle-35-hub-settings-value"}`)
		setSecNeedle := addNeedle("hub_settings", "secret", `{"s":"needle-36-hub-settings-secret"}`)
		require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{
			Key:    "enc.both",
			Value:  &setValNeedle,
			Secret: []byte(setSecNeedle),
		}))
		gotSetting, err := st.Settings().Get(ctx, "enc.both")
		require.NoError(t, err)
		require.NotNil(t, gotSetting.Value)
		assert.Equal(t, setValNeedle, *gotSetting.Value)
		assert.Equal(t, []byte(setSecNeedle), gotSetting.Secret)

		// Edge case: hub_settings with NULL value or NULL secret.
		valOnly := `{"only":"value"}`
		require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{Key: "enc.valonly", Value: &valOnly}))
		gotValOnly, err := st.Settings().Get(ctx, "enc.valonly")
		require.NoError(t, err)
		require.NotNil(t, gotValOnly.Value)
		assert.Equal(t, valOnly, *gotValOnly.Value)
		assert.Nil(t, gotValOnly.Secret)

		require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{Key: "enc.seconly", Secret: []byte(`{"only":"secret"}`)}))
		gotSecOnly, err := st.Settings().Get(ctx, "enc.seconly")
		require.NoError(t, err)
		assert.Nil(t, gotSecOnly.Value)
		assert.Equal(t, []byte(`{"only":"secret"}`), gotSecOnly.Secret)

		// 15. oauth_authorization_codes (4 encrypted columns: code_challenge, redirect_uri, granted_scopes, installation_name)
		authCodeHash := FixtureSecretHash("enc-auth-code")
		codeChallengeNeedle := addNeedle("oauth_authorization_codes", "code_challenge", "needle-40-oauth-auth-code-challenge")
		codeRedirectNeedle := addNeedle("oauth_authorization_codes", "redirect_uri", "https://needle-41-oauth-auth-code-redirect.example.com/cb")
		codeScopesNeedle := addNeedle("oauth_authorization_codes", "granted_scopes", "needle-42-oauth-auth-code-granted-scopes")
		codeInstNeedle := addNeedle("oauth_authorization_codes", "installation_name", "needle-43-oauth-auth-code-installation-name")
		require.NoError(t, st.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
			CodeHash:         authCodeHash,
			UserID:           uid,
			AuthGeneration:   1,
			ClientID:         clientID,
			CodeChallenge:    codeChallengeNeedle,
			RedirectURI:      codeRedirectNeedle,
			GrantedScopes:    codeScopesNeedle,
			InstallationName: codeInstNeedle,
			ExpiresAt:        now.Add(time.Minute),
			Redemption:       store.RedemptionKindToken,
		}))
		gotAuthCode, err := st.OAuthAuthorizationCodes().Get(ctx, authCodeHash)
		require.NoError(t, err)
		assert.Equal(t, codeChallengeNeedle, gotAuthCode.CodeChallenge)
		assert.Equal(t, codeRedirectNeedle, gotAuthCode.RedirectURI)
		assert.Equal(t, codeScopesNeedle, gotAuthCode.GrantedScopes)
		assert.Equal(t, codeInstNeedle, gotAuthCode.InstallationName)

		// 16. layout_event_heads (1 encrypted column: cursor_key)
		// 17. layout_event_entries (1 encrypted column: fact)
		cursorKeyNeedle := addNeedle("layout_event_heads", "cursor_key", "needle-44-layout-cursor-key-32by")
		require.Len(t, cursorKeyNeedle, 32)
		require.NoError(t, st.LayoutEvents().EnsureHead(ctx, uid, "enc-layout-journal", []byte(cursorKeyNeedle)))
		layoutFactNeedle := addNeedle("layout_event_entries", "fact", "needle-45-layout-event-entry-fact")
		_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
			Owner:              uid,
			ObservedUnixMillis: now.UnixMilli(),
			Facts:              [][]byte{[]byte(layoutFactNeedle)},
		})
		require.NoError(t, err)
		gotLayoutPage, err := st.LayoutEvents().ReadPage(ctx, uid, "", 0, 10)
		require.NoError(t, err)
		assert.Equal(t, []byte(cursorKeyNeedle), gotLayoutPage.Head.CursorKey)
		require.Len(t, gotLayoutPage.Rows, 1)
		assert.Equal(t, []byte(layoutFactNeedle), gotLayoutPage.Rows[0].Fact)

		// Confirm we tested every single encrypted column registered in sqlutil.
		totalCols := 0
		for _, tbl := range sqlutil.PersistentEncryptedTables {
			totalCols += len(tbl.Columns)
		}
		for _, tbl := range sqlutil.TransientEncryptedTables {
			totalCols += len(tbl.Columns)
		}
		require.Len(t, checks, totalCols, "test must cover every encrypted column in PersistentEncryptedTables + TransientEncryptedTables")

		for _, c := range checks {
			found, err := st.TestHelper().TableHoldsText(ctx, c.table, c.needle)
			require.NoError(t, err, "TableHoldsText(%s, %s)", c.table, c.column)
			assert.Falsef(t, found, "plaintext needle for %s.%s must not appear on disk", c.table, c.column)
		}
	})

	t.Run("key rotation and key maintenance across all 14 persistent tables and 1 transient table", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)

		ksV1, ksV12, ksV2 := setupRotationKeystores(t)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)

		// Seed 1 row in each of the 14 persistent tables under v1.
		userID := id.Generate()
		uid := userid.MustNew(userID)
		require.NoError(t, stV1.Users().Create(ctx, store.CreateUserParams{
			ID:                    userID,
			Username:              "rotuser",
			PasswordHash:          "rot-pw-hash",
			DisplayName:           "Rot User",
			Email:                 "rot@example.com",
			EmailVerified:         true,
			FirstCredentialExempt: true,
		}))
		require.NoError(t, stV1.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{ID: userID, Prefs: `{"rot":true}`}))
		pendingExp := now.Add(time.Hour)
		minted, err := stV1.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      userID,
			PendingEmail:            "rot-pending@example.com",
			PendingEmailToken:       "rot-pending-tok",
			PendingEmailExpiresAt:   &pendingExp,
			PendingEmailUnblockedAt: now.Add(time.Minute),
			Now:                     now,
		})
		mustSetPendingEmail(t, minted, err)

		sessID := id.Generate()
		require.NoError(t, stV1.Sessions().Create(ctx, store.CreateSessionParams{
			SecretHash: FixtureSecretHash("rot-sess"),
			ID:         sessID,
			UserID:     uid,
			ExpiresAt:  now.Add(24 * time.Hour),
			UserAgent:  "rot-ua",
			IPAddress:  "10.0.0.1",
		}))

		wsID := id.Generate()
		require.NoError(t, stV1.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID:          wsID,
			OwnerUserID: uid,
			Title:       "rot-workspace",
		}))

		secID := id.Generate()
		require.NoError(t, stV1.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      uid,
			Name:        "rot-section",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))

		stateParams, err := store.NewUpsertUserStateParams(&leapmuxv1.UserCrdtState{
			UserId:     userID,
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{"rot-ws": {}},
		})
		require.NoError(t, err)
		require.NoError(t, stV1.UserState().Upsert(ctx, stateParams))

		require.NoError(t, stV1.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID:             uid,
			PhysicalMs:         2000,
			Logical:            1,
			BatchID:            "rot-batch-1",
			BatchPayload:       []byte("rot-batch-payload"),
			TransitionsPayload: []byte("rot-trans-payload"),
			OpCount:            1,
		}))

		require.NoError(t, stV1.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
			UserID:  uid,
			OpType:  leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE,
			Payload: []byte("rot-outbox-payload"),
		}))

		worker := SeedWorker(t, stV1, userID)
		pubKey := store.WorkerTabPublicationKey{UserID: uid, WorkerID: worker.ID, TabID: "rot-tab-1"}
		require.NoError(t, stV1.WorkerTabPublications().Insert(ctx, store.InsertWorkerTabPublicationParams{
			WorkerTabPublicationKey: pubKey,
			WorkerTabPublication: store.WorkerTabPublication{
				RequestHash:     FixtureSecretHash("rot-pub-req"),
				ResponsePayload: []byte("rot-pub-resp"),
			},
		}))

		clientID := "rot-client-" + id.Generate()
		_, err = stV1.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
			ClientID:           clientID,
			OwnerUserID:        userID,
			ClientName:         "Rot Client",
			IconBlob:           []byte("rot-icon"),
			IconMediaType:      "image/png",
			ClientURI:          "https://rot.example.com",
			RedirectURIs:       "https://rot.example.com/cb",
			Scopes:             "read",
			GrantTypes:         "authorization_code",
			RegistrationSource: store.OAuthClientSourceUser,
		})
		require.NoError(t, err)

		apiTokID := id.Generate()
		require.NoError(t, stV1.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               apiTokID,
			UserID:           uid,
			ClientID:         clientID,
			InstallationName: "rot-cli",
			GrantedScopes:    "read",
			Issuance:         store.APITokenIssuanceAuthorizationCode,
			SecretHash:       FixtureSecretHash("rot-api-tok"),
		}))

		delTokID := id.Generate()
		require.NoError(t, stV1.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			ID:            delTokID,
			UserID:        uid,
			WorkerID:      worker.ID,
			GrantedScopes: "read",
			SecretHash:    FixtureSecretHash("rot-del-tok"),
			ExpiresAt:     now.Add(time.Hour),
		}))

		provID := id.Generate()
		require.NoError(t, stV1.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID:           provID,
			ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
			Name:         "Rot Provider",
			IssuerURL:    "https://idp.rot.example.com",
			ClientID:     "rot-idp-cid",
			ClientSecret: []byte("rot-idp-secret"),
			Scopes:       "openid",
			Enabled:      true,
		}))

		pkID := id.Generate()
		require.NoError(t, stV1.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           pkID,
			UserID:       userID,
			CredentialID: []byte("rot-cred-id"),
			PublicKey:    []byte("rot-pk-pub"),
			AAGUID:       []byte("rot-aaguid"),
			Transports:   "internal",
			FriendlyName: "Rot Key",
			CreatedAt:    now,
		}))

		settingVal := `{"k":"rot-val"}`
		require.NoError(t, stV1.Settings().Upsert(ctx, store.UpsertSettingParams{
			Key:    "rot.setting",
			Value:  &settingVal,
			Secret: []byte(`{"s":"rot-sec"}`),
		}))

		rotCursorKey := []byte("rot-layout-cursor-key-32-bytes!!")
		require.Len(t, rotCursorKey, 32)
		require.NoError(t, stV1.LayoutEvents().EnsureHead(ctx, uid, "rot-layout-journal", rotCursorKey))
		_, err = stV1.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
			Owner:              uid,
			ObservedUnixMillis: now.UnixMilli(),
			Facts:              [][]byte{[]byte("rot-layout-fact")},
		})
		require.NoError(t, err)

		// Seed 1 row in the transient table under v1.
		authCodeHash := FixtureSecretHash("rot-auth-code")
		require.NoError(t, stV1.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
			CodeHash:         authCodeHash,
			UserID:           uid,
			AuthGeneration:   1,
			ClientID:         clientID,
			CodeChallenge:    "rot-challenge",
			RedirectURI:      "https://rot.example.com/cb",
			GrantedScopes:    "read",
			InstallationName: "rot-inst",
			ExpiresAt:        now.Add(time.Minute),
			Redemption:       store.RedemptionKindToken,
		}))

		// Switch to {v1, v2} with active v2.
		stV12 := st.TestHelper().WithKeystore(ksV12)

		refs, err := stV12.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
		require.NoError(t, err)
		refByTable := make(map[string]int64, len(refs))
		for _, r := range refs {
			refByTable[r.Table] = r.Count
		}
		for _, spec := range sqlutil.PersistentEncryptedTables {
			assert.GreaterOrEqualf(t, refByTable[spec.Table], int64(1), "persistent table %s must report v1 refs", spec.Table)
		}
		for _, spec := range sqlutil.TransientEncryptedTables {
			assert.Zero(t, refByTable[spec.Table], "transient table %s must be excluded from CountKeyVersionRefs", spec.Table)
		}

		// Reencrypt all persistent rows to v2.
		res, err := stV12.KeyMaintenance().ReencryptAll(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint32(2), res.ActiveVersion)
		expectedUpdated := int64(len(sqlutil.PersistentEncryptedTables) + len(oauthapp.BuiltInRegistrations()))
		assert.Equal(t, expectedUpdated, res.RowsUpdated)

		// Version 1 must now have zero references across persistent tables.
		refsAfter, err := stV12.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
		require.NoError(t, err)
		assert.Empty(t, refsAfter)

		// Second ReencryptAll must be idempotent (0 rows updated).
		resAgain, err := stV12.KeyMaintenance().ReencryptAll(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), resAgain.RowsUpdated)
		assert.Equal(t, uint32(2), resAgain.ActiveVersion)

		// Switch to {v2} only: every persistent row reads back identically.
		stV2 := st.TestHelper().WithKeystore(ksV2)

		u, err := stV2.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "rot-pw-hash", u.PasswordHash)
		assert.Equal(t, `{"rot":true}`, u.Prefs)
		assert.Equal(t, "rot-pending-tok", u.PendingEmailToken)

		sess, err := stV2.Sessions().GetByID(ctx, sessID, now)
		require.NoError(t, err)
		assert.Equal(t, "rot-ua", sess.UserAgent)
		assert.Equal(t, "10.0.0.1", sess.IPAddress)

		ws, err := stV2.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "rot-workspace", ws.Title)

		sec, err := stV2.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, "rot-section", sec.Name)

		us, err := stV2.UserState().Get(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, stateParams.StatePayload(), us.StatePayload)

		batches, err := stV2.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{UserID: uid, Limit: 10})
		require.NoError(t, err)
		require.Len(t, batches, 1)
		assert.Equal(t, []byte("rot-batch-payload"), batches[0].BatchPayload)
		assert.Equal(t, []byte("rot-trans-payload"), batches[0].TransitionsPayload)

		outbox, err := stV2.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{UserID: uid, Limit: 10})
		require.NoError(t, err)
		require.Len(t, outbox, 1)
		assert.Equal(t, []byte("rot-outbox-payload"), outbox[0].Payload)

		pub, err := stV2.WorkerTabPublications().Get(ctx, pubKey)
		require.NoError(t, err)
		assert.Equal(t, []byte("rot-pub-resp"), pub.ResponsePayload)

		cli, err := stV2.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		assert.Equal(t, "Rot Client", cli.ClientName)
		cliIcon, err := stV2.OAuthClients().GetIcon(ctx, clientID)
		require.NoError(t, err)
		assert.Equal(t, []byte("rot-icon"), cliIcon.IconBlob)
		assert.Equal(t, "image/png", cliIcon.IconMediaType)
		// Built-in client must also decrypt under v2 only.
		builtIn, err := stV2.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
		require.NoError(t, err)
		assert.NotEmpty(t, builtIn.ClientName)

		apiTok, err := stV2.APITokens().GetByID(ctx, apiTokID)
		require.NoError(t, err)
		assert.Equal(t, "rot-cli", apiTok.InstallationName)
		assert.Equal(t, "read", apiTok.GrantedScopes)

		delTok, err := stV2.DelegationTokens().GetByID(ctx, delTokID)
		require.NoError(t, err)
		assert.Equal(t, "read", delTok.GrantedScopes)

		prov, err := stV2.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.Equal(t, "Rot Provider", prov.Name)
		assert.Equal(t, []byte("rot-idp-secret"), prov.ClientSecret)

		pk, err := stV2.PasskeyCredentials().GetByID(ctx, pkID)
		require.NoError(t, err)
		assert.Equal(t, []byte("rot-pk-pub"), pk.PublicKey)
		assert.Equal(t, []byte("rot-aaguid"), pk.AAGUID)
		assert.Equal(t, "Rot Key", pk.FriendlyName)

		settingRow, err := stV2.Settings().Get(ctx, "rot.setting")
		require.NoError(t, err)
		require.NoError(t, settingRow.ValueDecryptErr)
		require.NoError(t, settingRow.SecretDecryptErr)
		require.NotNil(t, settingRow.Value)
		assert.Equal(t, settingVal, *settingRow.Value)
		assert.Equal(t, []byte(`{"s":"rot-sec"}`), settingRow.Secret)

		rotLayoutPage, err := stV2.LayoutEvents().ReadPage(ctx, uid, "", 0, 10)
		require.NoError(t, err)
		assert.Equal(t, rotCursorKey, rotLayoutPage.Head.CursorKey)
		require.Len(t, rotLayoutPage.Rows, 1)
		assert.Equal(t, []byte("rot-layout-fact"), rotLayoutPage.Rows[0].Fact)

		// The transient table was NOT re-encrypted, so reads under {v2} only must fail decryption.
		_, err = stV2.OAuthAuthorizationCodes().Get(ctx, authCodeHash)
		require.Error(t, err, "oauth_authorization_codes row must remain on v1")
	})

	t.Run("partial-column update on multi-column table", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)

		ksV1, ksV12, ksV2 := setupRotationKeystores(t)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)

		userID := id.Generate()
		require.NoError(t, stV1.Users().Create(ctx, store.CreateUserParams{
			ID:                    userID,
			Username:              "partialuser",
			PasswordHash:          "partial-v1-password-hash",
			DisplayName:           "Partial User",
			Email:                 "partial@example.com",
			EmailVerified:         true,
			FirstCredentialExempt: true,
		}))

		// Update only users.prefs under v2 while users.password_hash stays at v1.
		stV12 := st.TestHelper().WithKeystore(ksV12)
		require.NoError(t, stV12.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
			ID:    userID,
			Prefs: `{"theme":"dark","v":2}`,
		}))

		refs, err := stV12.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
		require.NoError(t, err)
		var usersCount int64
		for _, r := range refs {
			if r.Table == "users" {
				usersCount = r.Count
			}
		}
		assert.Equal(t, int64(1), usersCount, "users must still report v1 ref for password_hash")

		res, err := stV12.KeyMaintenance().ReencryptAll(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, res.RowsUpdated, int64(1))

		refsAfter, err := stV12.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
		require.NoError(t, err)
		assert.Empty(t, refsAfter)

		stV2 := st.TestHelper().WithKeystore(ksV2)
		got, err := stV2.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "partial-v1-password-hash", got.PasswordHash)
		assert.Equal(t, `{"theme":"dark","v":2}`, got.Prefs)
	})

	t.Run("cross-row ciphertext swap rejected by AAD", func(t *testing.T) {
		st := s.NewStore(t)
		u := SeedUser(t, st, "aadswapuser")
		wsA := SeedWorkspace(t, st, u.ID, "Workspace Alpha")
		wsB := SeedWorkspace(t, st, u.ID, "Workspace Beta")

		require.NoError(t, st.TestHelper().CopyWorkspaceTitleCiphertext(ctx, wsA, wsB))

		_, err := st.Workspaces().GetByID(ctx, wsB)
		require.Error(t, err, "swapped ciphertext from workspace A must fail AAD verification on workspace B")

		gotA, err := st.Workspaces().GetByID(ctx, wsA)
		require.NoError(t, err)
		assert.Equal(t, "Workspace Alpha", gotA.Title)
	})

	t.Run("key maintenance inside caller transaction and borrowed WithKeystore close safety", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)
		ksV1, ksV12, ksV2 := setupRotationKeystores(t)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)

		// Closing a borrowed WithKeystore handle must be a no-op and leave st open.
		require.NoError(t, stV1.Close())

		u := SeedUser(t, stV1, "intx-km-user")
		wsID := SeedWorkspace(t, stV1, u.ID, "InTx Workspace")

		stV12 := st.TestHelper().WithKeystore(ksV12)
		require.NoError(t, stV12.RunInTransaction(ctx, func(tx store.Store) error {
			refs, err := tx.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
			if err != nil {
				return err
			}
			require.NotEmpty(t, refs, "in-tx CountKeyVersionRefs must see v1 refs")

			res, err := tx.KeyMaintenance().ReencryptAll(ctx)
			if err != nil {
				return err
			}
			assert.GreaterOrEqual(t, res.RowsUpdated, int64(2))

			refsAfter, err := tx.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
			if err != nil {
				return err
			}
			assert.Empty(t, refsAfter, "in-tx CountKeyVersionRefs must see re-encrypted writes")
			return nil
		}))

		stV2 := st.TestHelper().WithKeystore(ksV2)
		gotWS, err := stV2.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "InTx Workspace", gotWS.Title)
	})

	t.Run("plaintext user mutations succeed when encrypted columns cannot decrypt", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)
		ksV1, _, ksV2 := setupRotationKeystores(t)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)
		adminA := SeedUser(t, stV1, "adm-plain-a")
		adminB := SeedUser(t, stV1, "adm-plain-b")
		require.NoError(t, stV1.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: adminA.ID, IsAdmin: true}))
		require.NoError(t, stV1.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: adminB.ID, IsAdmin: true}))

		// Under v2-only, adminB's password_hash cannot decrypt, yet UpdateAdmin
		// (which uses GetLiveUserForUpdate) must still succeed.
		stV2 := st.TestHelper().WithKeystore(ksV2)
		require.NoError(t, stV2.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: adminB.ID, IsAdmin: false}))
	})

	t.Run("VerifyKeyring and Migrator protect built-ins against wrong or incomplete key ring", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)
		ksV1, ksV12, ksV2 := setupRotationKeystores(t)
		ksWrongV1, err := keystore.NewRandom()
		require.NoError(t, err)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)
		_ = SeedUser(t, stV1, "verify-kr-user")

		// Matching rings succeed (both v1-only and dual {v1,v2}).
		require.NoError(t, stV1.KeyMaintenance().VerifyKeyring(ctx))
		require.NoError(t, st.TestHelper().WithKeystore(ksV12).KeyMaintenance().VerifyKeyring(ctx))

		// Unretained version (v2-only) fails both VerifyKeyring and Migrate without touching built-ins.
		stV2 := st.TestHelper().WithKeystore(ksV2)
		require.Error(t, stV2.KeyMaintenance().VerifyKeyring(ctx))
		require.Error(t, stV2.Migrator().Migrate(ctx))

		// Wrong v1 key material (same version number, different key bytes) fails both without touching built-ins.
		stWrongV1 := st.TestHelper().WithKeystore(ksWrongV1)
		require.Error(t, stWrongV1.KeyMaintenance().VerifyKeyring(ctx))
		require.Error(t, stWrongV1.Migrator().Migrate(ctx))

		// Built-in oauth_clients must remain intact and readable under the original v1 keystore.
		for _, app := range oauthapp.BuiltInRegistrations() {
			got, getErr := stV1.OAuthClients().Get(ctx, app.ClientID)
			require.NoError(t, getErr, "built-in %s must not be overwritten by failed wrong-key Migrate", app.ClientID)
			assert.Equal(t, app.ClientName, got.ClientName)
		}
	})

	t.Run("GetAuthIdentity and ListAll/Search skip unused encrypted user columns", func(t *testing.T) {
		st := s.NewStore(t)
		now := time.Now().UTC().Truncate(time.Second)
		ksV1, _, ksV2 := setupRotationKeystores(t)

		stV1 := useSwappedKeystore(t, ctx, st, ksV1, now)

		u := SeedUser(t, stV1, "narrow-proj-user")
		require.NoError(t, stV1.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
			ID:    u.ID,
			Prefs: `{"theme":"dark"}`,
		}))
		exp := now.Add(time.Hour)
		ok, err := stV1.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      u.ID,
			PendingEmail:            "narrow@example.com",
			PendingEmailToken:       "ABC-123",
			PendingEmailExpiresAt:   &exp,
			PendingEmailUnblockedAt: now,
			Now:                     now,
		})
		require.NoError(t, err)
		require.True(t, ok)

		// Under v2-only, none of the encrypted columns can decrypt, yet GetAuthIdentity
		// (used by bearer auth) must succeed.
		stV2 := st.TestHelper().WithKeystore(ksV2)
		ident, err := stV2.Users().GetAuthIdentity(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, u.ID, ident.ID)
		assert.Equal(t, u.Username, ident.Username)

		// Re-encrypt ONLY password_hash under v2 while prefs and pending_email_token
		// stay encrypted under v1. Full GetByID must fail on prefs/pending_email_token,
		// whereas ListAll and Search must succeed and report HasUsablePassword().
		require.NoError(t, stV2.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			ID:           u.ID,
			PasswordHash: "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		}))

		_, err = stV2.Users().GetByID(ctx, u.ID)
		require.Error(t, err, "full GetByID must still fail when prefs/pending_email_token cannot decrypt")

		listPage, err := stV2.Users().ListAll(ctx, store.ListAllUsersParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err, "ListAll must skip decrypting prefs and pending_email_token")
		require.Len(t, listPage.Rows, 1)
		assert.Equal(t, u.ID, listPage.Rows[0].ID)
		assert.True(t, listPage.Rows[0].HasUsablePassword())

		q := "narrow-proj"
		searchPage, err := stV2.Users().Search(ctx, store.SearchUsersParams{
			Query:      &q,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err, "Search must skip decrypting prefs and pending_email_token")
		require.Len(t, searchPage.Rows, 1)
		assert.Equal(t, u.ID, searchPage.Rows[0].ID)
		assert.True(t, searchPage.Rows[0].HasUsablePassword())
	})
}
