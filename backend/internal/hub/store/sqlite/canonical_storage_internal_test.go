package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllDatetimeColumnsStoreCanonicalLayout drives every write path that
// binds a Go-side time into a DATETIME column and then asserts -- via the
// schema-discovered CheckCanonicalTimestamps walk -- that every stored value
// is the canonical 24-char strftime('%Y-%m-%dT%H:%M:%fZ') layout. Binds use a
// deliberately non-UTC fixed zone: a raw time.Time bind would store modernc's
// driver layout with the zone's offset (space at byte 11, '+09:00' suffix),
// so any write path that binds a raw time.Time instead of a sqltime.SQLiteTime
// (or misses its SQL-side strftime wrap) fails here instead of silently splitting
// the store into two timestamp layouts whose raw-string compares diverge as
// soon as the offsets differ.
//
// The test also asserts non-vacuity: every discovered DATETIME column --
// NOT NULL and nullable alike -- must hold at least one non-null value by the
// end of the fixtures, so no column's layout contract passes merely because
// nothing was ever stored in it. A new DATETIME column therefore fails this
// test until a fixture write for it is added here.
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

	// delegation_tokens: expires_at + refresh_expires_at on Create (created_at
	// via its column DEFAULT); last_used_at and revoked_at are exercised by
	// the Touch/Revoke fixtures further down.
	delegationID := id.Generate()
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID:               delegationID,
		UserID:           userid.MustNew(user.ID),
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
		UserID:           userid.MustNew(user.ID),
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
		UserID:     userid.MustNew(user.ID),
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
		UserID:       userid.MustNew(user.ID),
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
		UserID:        userid.MustNew(user.ID),
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
		UserID:     userid.MustNew(user.ID),
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

	// user_sessions: expires_at is Go-bound by Create; created_at and
	// last_active_at fill via their column DEFAULTs.
	storetest.SeedSession(t, st, user.ID)

	// worker_registration_keys: created_at DEFAULT + Go-bound expires_at.
	storetest.SeedRegistrationKey(t, st, user.ID, future)

	// worker_notifications: created_at DEFAULT on Create, delivered_at via
	// MarkDelivered's strftime.
	notifID := id.Generate()
	require.NoError(t, st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
		ID:       notifID,
		WorkerID: worker.ID,
		Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
		Payload:  `{"reason":"canon"}`,
	}))
	require.NoError(t, st.WorkerNotifications().MarkDelivered(ctx, notifID))

	// org_op_batches.committed_at via its column DEFAULT.
	require.NoError(t, st.OrgOpBatches().Insert(ctx, store.InsertOrgOpBatchParams{
		OrgID:        orgID,
		PhysicalMs:   1,
		Logical:      1,
		LastLogical:  1,
		OriginClient: "client",
		PrincipalID:  user.ID,
		BatchID:      "canon-opbatch",
		BodyHash:     []byte("hash"),
		BatchPayload: []byte("payload"),
		OpCount:      1,
		Epoch:        1,
	}))

	// workspace_sections.created_at via its column DEFAULT.
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userid.MustNew(user.ID),
		Name:        "canon-section",
		Position:    "a0",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))

	// oauth_user_links.created_at via its column DEFAULT.
	require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID:          userid.MustNew(user.ID),
		ProviderID:      provider.ID,
		ProviderSubject: "canon-subject",
	}))

	// users.pending_email_expires_at is Go-bound by SetPendingEmail.
	require.NoError(t, st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    user.ID,
		PendingEmail:          "canon-pending@example.com",
		PendingEmailToken:     "canon-token",
		PendingEmailExpiresAt: ptr(future),
	}))

	// workers.last_seen_at via UpdateLastSeen's strftime.
	require.NoError(t, st.Workers().UpdateLastSeen(ctx, worker.ID))

	// api_tokens.last_used_at via Touch.
	require.NoError(t, st.APITokens().Touch(ctx, rotatedID))

	// delegation_tokens: last_used_at via Touch, revoked_at via Revoke.
	require.NoError(t, st.DelegationTokens().Touch(ctx, delegationID))
	revokedDeleg, err := st.DelegationTokens().Revoke(ctx, delegationID)
	require.NoError(t, err)
	require.EqualValues(t, 1, revokedDeleg)

	// users.tokens_revoked_at via RevokeUserTokens, which also enqueues
	// another pending revocation event.
	revokedUsers, err := st.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
	require.NoError(t, err)
	require.EqualValues(t, 1, revokedUsers)

	// hub_runtime_lease.lease_expires_at via AcquireHubRuntimeLease's SQL-side
	// strftime offset; the same call publishes the pending revocation events
	// enqueued by the Revoke calls above, filling
	// revocation_events.published_at.
	_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
		HolderID:      "canon-holder",
		PublishLimit:  100,
		LeaseDuration: time.Hour,
	})
	require.NoError(t, err)

	// Soft-delete paths, run last so every fixture above wrote against live
	// rows: users.deleted_at + orgs.deleted_at through the transactional user
	// delete (which soft-deletes the personal org too); workers.deleted_at and
	// workspaces.deleted_at through their own strftime-based soft deletes.
	delOrgID := storetest.SeedOrg(t, st, "canon-org-del")
	delUser := storetest.SeedUser(t, st, delOrgID, "canon-user-del")
	require.NoError(t, st.Users().Delete(ctx, delUser.ID))
	require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
	deletedWs, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(user.ID),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, deletedWs)

	require.NoError(t, CheckCanonicalTimestamps(ctx, testable))

	// Non-vacuity: a column with zero non-null rows passes the layout probe
	// without ever being exercised, so a raw time.Time bind on that write path
	// would ship unnoticed. Every discovered column must hold at least one
	// value by the end of the fixture writes above. This assertion lives here
	// rather than in CheckCanonicalTimestamps because no single storetest
	// subtest writes every table -- only this test's fixtures do.
	db := testable.(*testableSQLiteStore).conn.shared.db
	_, columns, err := sqlitedb.FindNonCanonicalDatetimes(ctx, db, "goose_db_version")
	require.NoError(t, err)
	assert.Empty(t, sqlitedb.UncoveredColumns(columns),
		"DATETIME column(s) with zero non-null rows -- their canonical-layout check passed vacuously; add a fixture write for each so the contract is actually exercised")
}
