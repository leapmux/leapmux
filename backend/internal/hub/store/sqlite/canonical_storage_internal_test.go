package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/require"
)

// TestAllDatetimeColumnsStoreCanonicalLayout drives every write path that
// binds a Go-side time into a DATETIME column and then asserts -- via the
// schema-discovered CheckCanonicalTimestamps walk -- that every stored value
// is the canonical 24-char strftime('%Y-%m-%dT%H:%M:%fZ') layout. Binds use a
// deliberately non-UTC fixed zone: a raw time.Time bind would store modernc's
// driver layout with the zone's offset (space at byte 11, '+09:00' suffix),
// so any write path missing its SQL-side strftime wrap (or Go-side
// formatSQLiteTime pre-formatting) fails here instead of silently splitting
// the store into two timestamp layouts whose raw-string compares diverge as
// soon as the offsets differ.
func TestAllDatetimeColumnsStoreCanonicalLayout(t *testing.T) {
	ctx := context.Background()
	testable, err := OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testable.Close()) })
	st := store.Store(testable)

	// Non-UTC on purpose; see the doc comment.
	zone := time.FixedZone("UTC+9", 9*60*60)
	now := time.Now().In(zone)
	future := now.Add(time.Hour)
	farFuture := now.Add(2 * time.Hour)
	ptr := func(v time.Time) *time.Time { return &v }

	orgID := storetest.SeedOrg(t, st, "canon-org")
	user := storetest.SeedUser(t, st, orgID, "canon-user")
	worker := storetest.SeedWorker(t, st, user.ID)
	workspaceID := storetest.SeedWorkspace(t, st, orgID, user.ID, "canon-ws")
	provider := storetest.SeedOAuthProvider(t, st, "canon-provider")

	// delegation_tokens: expires_at + refresh_expires_at.
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID:               id.Generate(),
		UserID:           user.ID,
		WorkerID:         worker.ID,
		WorkspaceID:      workspaceID,
		SecretHash:       []byte("dt-secret"),
		RefreshHash:      []byte("dt-refresh"),
		ExpiresAt:        future,
		RefreshExpiresAt: ptr(farFuture),
	}))

	// api_tokens: expires_at + refresh_expires_at on Create, the New*/Prev*
	// triplet on RotateRefresh, and revocation_events.revoked_at via Revoke.
	rotatedID := id.Generate()
	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:               rotatedID,
		UserID:           user.ID,
		ClientType:       "cli",
		ClientName:       "canon-client",
		SecretHash:       []byte("at-secret"),
		RefreshHash:      []byte("at-refresh"),
		Scope:            "remote:*",
		ExpiresAt:        ptr(future),
		RefreshExpiresAt: ptr(farFuture),
	}))
	rotated, err := st.APITokens().RotateRefresh(ctx, store.RotateAPITokenRefreshParams{
		ID:                       rotatedID,
		NewSecretHash:            []byte("at-secret-2"),
		NewExpiresAt:             ptr(future.Add(time.Minute)),
		NewRefreshHash:           []byte("at-refresh-2"),
		NewRefreshExpiresAt:      ptr(farFuture.Add(time.Minute)),
		PreviousRefreshHash:      []byte("at-refresh"),
		PreviousRefreshExpiresAt: ptr(future),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rotated)
	revokedID := id.Generate()
	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:         revokedID,
		UserID:     user.ID,
		ClientType: "cli",
		ClientName: "canon-client-revoked",
		SecretHash: []byte("at-secret-3"),
		Scope:      "remote:*",
		ExpiresAt:  ptr(future),
	}))
	revoked, err := st.APITokens().Revoke(ctx, revokedID)
	require.NoError(t, err)
	require.EqualValues(t, 1, revoked)

	// oauth_states.expires_at.
	require.NoError(t, st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
		State:        "canon-state",
		ProviderID:   provider.ID,
		PkceVerifier: "verifier",
		ExpiresAt:    future,
	}))

	// oauth_tokens: expires_at on insert, then the ON CONFLICT branch, which
	// rewrites updated_at.
	upsert := store.UpsertOAuthTokensParams{
		UserID:       user.ID,
		ProviderID:   provider.ID,
		AccessToken:  []byte("access"),
		RefreshToken: []byte("refresh"),
		TokenType:    "Bearer",
		ExpiresAt:    future,
		KeyVersion:   1,
	}
	require.NoError(t, st.OAuthTokens().Upsert(ctx, upsert))
	upsert.ExpiresAt = farFuture
	require.NoError(t, st.OAuthTokens().Upsert(ctx, upsert))

	// pending_oauth_signups: token_expires_at + expires_at.
	require.NoError(t, st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
		Token:           "canon-signup",
		ProviderID:      provider.ID,
		ProviderSubject: "subject",
		AccessToken:     []byte("access"),
		RefreshToken:    []byte("refresh"),
		TokenType:       "Bearer",
		TokenExpiresAt:  future,
		KeyVersion:      1,
		ExpiresAt:       farFuture,
	}))

	// cli_authorization_codes: expires_at on Create, consumed_at on Consume.
	require.NoError(t, st.CLIAuthorizationCodes().Create(ctx, store.CreateCLIAuthorizationCodeParams{
		Code:          "canon-code",
		UserID:        user.ID,
		CodeChallenge: "challenge",
		ExpiresAt:     future,
	}))
	_, err = st.CLIAuthorizationCodes().Consume(ctx, "canon-code")
	require.NoError(t, err)

	// device_authorizations: expires_at on Create, last_polled_at on
	// TouchPoll, consumed_at on Consume.
	require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
		DeviceCode:      "canon-device-code",
		UserCode:        "CANON-USER-CODE",
		IntervalSeconds: 5,
		ExpiresAt:       future,
	}))
	require.NoError(t, st.DeviceAuthorizations().TouchPoll(ctx, "canon-device-code"))
	approved, err := st.DeviceAuthorizations().Approve(ctx, store.ApproveDeviceAuthorizationParams{
		DeviceCode: "canon-device-code",
		UserID:     user.ID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, approved)
	consumed, err := st.DeviceAuthorizations().Consume(ctx, "canon-device-code")
	require.NoError(t, err)
	require.EqualValues(t, 1, consumed)

	// lifecycle_outbox.consumed_at.
	require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
		OrgID:   orgID,
		OpType:  "create",
		Payload: []byte("payload"),
	}))
	pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
		OrgID: orgID,
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, st.LifecycleOutbox().MarkConsumed(ctx, store.MarkLifecycleOutboxConsumedParams{
		ID:         pending[0].ID,
		ConsumedAt: now,
	}))

	// org_recent_batch_ids.expires_at.
	require.NoError(t, st.OrgRecentBatchIDs().Insert(ctx, store.InsertOrgRecentBatchIDParams{
		OrgID:               orgID,
		BatchID:             "canon-batch",
		BodyHash:            []byte("hash"),
		PrincipalID:         user.ID,
		CanonicalPhysicalMs: 1,
		CanonicalLogical:    1,
		CanonicalClient:     "client",
		OpCount:             1,
		Epoch:               1,
		ExpiresAt:           future,
	}))

	// org_state: epoch_started_at + updated_at on Upsert AND AdvanceEpoch.
	require.NoError(t, st.OrgState().Upsert(ctx, store.UpsertOrgStateParams{
		OrgID:          orgID,
		StatePayload:   []byte("state"),
		CurrentEpoch:   1,
		EpochStartedAt: now,
		UpdatedAt:      now,
	}))
	require.NoError(t, st.OrgState().AdvanceEpoch(ctx, store.AdvanceOrgEpochParams{
		OrgID:          orgID,
		Epoch:          2,
		EpochStartedAt: future,
		UpdatedAt:      future,
	}))

	require.NoError(t, CheckCanonicalTimestamps(ctx, testable))
}
