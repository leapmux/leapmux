package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundaryZone keeps every bound instant in this group deliberately non-UTC:
// each dialect must normalize the offset itself (SQLite via SQLiteTime's
// canonical UTC serialization, Postgres/MySQL via their valuers' UTC
// normalization), so a write path
// that started storing local wall time would shift every boundary by the
// offset and fail these pins loudly.
var boundaryZone = time.FixedZone("UTC+9", 9*60*60)

// boundaryCutoff returns a whole-millisecond "now" in boundaryZone. All three
// dialects store millisecond-or-finer precision (SQLite canonical strftime
// ms, MySQL DATETIME(3) ms with rounding, Postgres timestamptz us), so
// ms-aligned instants survive storage exactly and the strict-< semantics
// below are unambiguous.
func boundaryCutoff() time.Time {
	return time.Now().Truncate(time.Millisecond).In(boundaryZone)
}

// assertBoundarySweep pins the shared strict-< contract of a cutoff sweep:
// with rows planted at cutoff-1ms / cutoff / cutoff+1ms, the exact-cutoff
// sweep removes only the strictly-older row, and a far-future sweep then
// removes the two same-instant survivors -- proving the first sweep kept
// them, for tables with no unfiltered enumeration to inspect directly.
func assertBoundarySweep(t *testing.T, cutoff time.Time, sweep func(time.Time) (int64, error)) {
	t.Helper()
	swept, err := sweep(cutoff)
	require.NoError(t, err)
	assert.EqualValues(t, 1, swept, "only the row strictly before the cutoff may fall at the exact-cutoff sweep")
	swept, err = sweep(cutoff.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2, swept, "both same-instant survivors must still exist for the far-future sweep")
}

// testCleanupBoundaries pins the millisecond-exact boundary semantics of the
// cutoff-driven sweeps across every dialect: `col < cutoff` deletes the
// strictly-older row, keeps the exact-cutoff row, and keeps newer rows, even
// when all of them share the same second. The coarse multi-day gaps in
// testCleanup are exactly the shape that let a mixed-layout cutoff bind (which
// missed every same-day row on SQLite) reach a release with the suite still
// green. The users hard-delete is pinned here too (its FK restriction is
// testCleanup's job), since its predicate is hand-written per dialect rather
// than shared with the workspace/worker twins.
//
// Where a table has no unfiltered GetByID to enumerate survivors, a second
// sweep with a far-future cutoff pins the survivor count instead: it must
// delete exactly the rows the first sweep correctly kept.
func (s *Suite) testCleanupBoundaries(t *testing.T) {
	t.Run("delegation token expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		worker := SeedWorker(t, st, user.ID)
		cutoff := boundaryCutoff()

		create := func(expiresAt time.Time) string {
			tokenID := id.Generate()
			require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
				GrantedScopes: "workspace:read workspace:write worker:read",
				ID:            tokenID,
				UserID:        userid.MustNew(user.ID),
				WorkerID:      worker.ID,
				SecretHash:    []byte("secret"),
				ExpiresAt:     expiresAt,
			}))
			return tokenID
		}
		expiredID := create(cutoff.Add(-time.Millisecond))
		atCutoffID := create(cutoff)
		liveID := create(cutoff.Add(time.Millisecond))
		// Revoked rows are out of scope for the expiry sweep (revoked_at IS
		// NULL filter): the revoked-token sweep owns them, so revoking must
		// shield an otherwise-expired row.
		revokedID := create(cutoff.Add(-time.Millisecond))
		n, err := st.DelegationTokens().Revoke(ctx, revokedID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)

		deleted, err := st.Cleanup().DeleteExpiredDelegationTokensBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)

		_, err = st.DelegationTokens().GetByID(ctx, expiredID)
		assert.ErrorIs(t, err, store.ErrNotFound)
		for _, keep := range []string{atCutoffID, liveID, revokedID} {
			_, err := st.DelegationTokens().GetByID(ctx, keep)
			assert.NoError(t, err)
		}
	})

	// A LIVE api_token that can no longer authenticate AND can no longer
	// renew never works again, so the row only records history -- but a user
	// asking days later why their CLI stopped working is exactly who the
	// retention margin exists for. The sweep must therefore be strict at the
	// cutoff, on BOTH deadlines.
	t.Run("expired api-token sweep is strict at both deadlines", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "expired-token-user")
		cutoff := boundaryCutoff()
		open := cutoff.Add(time.Hour)

		seed := func(name string, expiresAt, refreshExpiresAt *time.Time) string {
			tokenID := id.Generate()
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID: tokenID, UserID: userid.MustNew(user.ID), ClientID: oauthapp.ControlCLIClientID, InstallationName: name, GrantedScopes: authscope.NonAdminGrant().String(),
				SecretHash: []byte("secret"), ExpiresAt: expiresAt, RefreshExpiresAt: refreshExpiresAt,
			}))
			return tokenID
		}

		// Both deadlines sit EXACTLY on the cutoff, which the strict < must
		// keep and one millisecond past must take.
		expired := seed("dead", &cutoff, &cutoff)
		// The refresh window is still open.
		liveRefresh := seed("live-refresh", &cutoff, &open)
		// The ACCESS token is still live. Bearer validation reads expires_at
		// alone, so this credential still authenticates however long ago its
		// refresh window closed -- the admin-issued shape.
		liveAccess := seed("live-access", &open, &cutoff)
		// No refresh deadline at all: a closed deadline, not an open one, so
		// the access expiry alone decides. Here it is past, so the row goes
		// with the second sweep below.
		noRefresh := seed("no-refresh", &cutoff, nil)
		// No access expiry at all: the row never stops authenticating, so it
		// is never swept here.
		neverExpires := seed("never-expires", nil, &cutoff)

		deleted, err := st.Cleanup().DeleteExpiredAPITokensBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 0, deleted, "exact-cutoff rows must survive the strict <")

		deleted, err = st.Cleanup().DeleteExpiredAPITokensBefore(ctx, cutoff.Add(time.Millisecond))
		require.NoError(t, err)
		assert.EqualValues(t, 2, deleted, "the two rows past both deadlines go, and only those")

		for _, gone := range []string{expired, noRefresh} {
			_, err = st.APITokens().GetByID(ctx, gone)
			assert.ErrorIs(t, err, store.ErrNotFound)
		}
		_, err = st.APITokens().GetByID(ctx, liveRefresh)
		assert.NoError(t, err, "a credential that can still refresh must survive")
		_, err = st.APITokens().GetByID(ctx, liveAccess)
		assert.NoError(t, err, "a credential whose access token still authenticates must survive")
		_, err = st.APITokens().GetByID(ctx, neverExpires)
		assert.NoError(t, err, "a NULL access expiry is not a past one")
	})

	t.Run("revoked token sweeps are strict at the stored revoke instant", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		worker := SeedWorker(t, st, user.ID)

		// revoked_at is written DB-side (NOW/strftime), so read the stored
		// instant back and place cutoffs exactly on and just past it: the
		// sweep must be a strict < against the value the revoke path stored,
		// whatever the dialect's precision.
		apiID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               apiID,
			UserID:           userid.MustNew(user.ID),
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "boundary-client",
			GrantedScopes:    authscope.NonAdminGrant().String(),
			SecretHash:       []byte("secret"),
		}))
		n, err := st.APITokens().Revoke(ctx, apiID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		apiTok, err := st.APITokens().GetByID(ctx, apiID)
		require.NoError(t, err)
		require.NotNil(t, apiTok.RevokedAt)

		deleted, err := st.Cleanup().DeleteRevokedAPITokensBefore(ctx, *apiTok.RevokedAt)
		require.NoError(t, err)
		assert.EqualValues(t, 0, deleted, "exact-cutoff revoked api token must survive the strict <")
		deleted, err = st.Cleanup().DeleteRevokedAPITokensBefore(ctx, apiTok.RevokedAt.Add(time.Millisecond))
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)

		delID := id.Generate()
		require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			GrantedScopes: "workspace:read workspace:write worker:read",
			ID:            delID,
			UserID:        userid.MustNew(user.ID),
			WorkerID:      worker.ID,
			SecretHash:    []byte("secret"),
			ExpiresAt:     boundaryCutoff().Add(time.Hour),
		}))
		n, err = st.DelegationTokens().Revoke(ctx, delID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		delTok, err := st.DelegationTokens().GetByID(ctx, delID)
		require.NoError(t, err)
		require.NotNil(t, delTok.RevokedAt)

		deleted, err = st.Cleanup().DeleteRevokedDelegationTokensBefore(ctx, *delTok.RevokedAt)
		require.NoError(t, err)
		assert.EqualValues(t, 0, deleted, "exact-cutoff revoked delegation token must survive the strict <")
		deleted, err = st.Cleanup().DeleteRevokedDelegationTokensBefore(ctx, delTok.RevokedAt.Add(time.Millisecond))
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)
	})

	t.Run("cli authorization code expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		cutoff := boundaryCutoff()

		for _, c := range []struct {
			code      string
			expiresAt time.Time
		}{
			{"code-expired", cutoff.Add(-time.Millisecond)},
			{"code-at-cutoff", cutoff},
			{"code-live", cutoff.Add(time.Millisecond)},
		} {
			require.NoError(t, st.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
				ClientID:      oauthapp.ControlCLIClientID,
				GrantedScopes: authscope.NonAdminGrant().String(),
				Code:          c.code,
				UserID:        userid.MustNew(user.ID),
				CodeChallenge: "challenge",
				ExpiresAt:     c.expiresAt,
			}))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().DeleteExpiredOAuthAuthorizationCodes(ctx, c)
		})
	})

	t.Run("device authorization expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := boundaryCutoff()

		for _, d := range []struct {
			suffix    string
			expiresAt time.Time
		}{
			{"expired", cutoff.Add(-time.Millisecond)},
			{"at-cutoff", cutoff},
			{"live", cutoff.Add(time.Millisecond)},
		} {
			require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
				ClientID:        oauthapp.ControlCLIClientID,
				DeviceCode:      "device-" + d.suffix,
				UserCode:        "USER-" + d.suffix,
				IntervalSeconds: 5,
				ExpiresAt:       d.expiresAt,
			}))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().DeleteExpiredDeviceAuthorizations(ctx, c)
		})
	})

	t.Run("registration key expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		cutoff := boundaryCutoff()

		SeedRegistrationKey(t, st, user.ID, cutoff.Add(-time.Millisecond))
		SeedRegistrationKey(t, st, user.ID, cutoff)
		SeedRegistrationKey(t, st, user.ID, cutoff.Add(time.Millisecond))

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, c)
		})
	})

	t.Run("workspace soft-delete sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		cutoff := boundaryCutoff()

		for i, deletedAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			wsID := SeedWorkspace(t, st, user.ID, "boundary-ws")
			_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
				ID:          wsID,
				OwnerUserID: userid.MustNew(user.ID),
			})
			require.NoError(t, err, "workspace %d", i)
			require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkspaces, wsID, deletedAt))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().HardDeleteWorkspacesBefore(ctx, c)
		})
	})

	t.Run("worker soft-delete sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		cutoff := boundaryCutoff()

		for _, deletedAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			worker := SeedWorker(t, st, user.ID)
			require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
			require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, worker.ID, deletedAt))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().HardDeleteWorkersBefore(ctx, c)
		})
	})

	// The users sweep is hand-written per dialect (`u.deleted_at <
	// sqlc.arg(cutoff)` on SQLite, `u.deleted_at < $1` on Postgres,
	// `users.deleted_at < ?` on MySQL), so a `<` -> `<=` slip in ONE dialect is
	// invisible to testCleanup, whose users case plants a 48h-old row against a
	// 24h cutoff. Each fixture user is seeded FK-free (no workspace, no worker),
	// so the NOT EXISTS gate clauses in HardDeleteUsersBefore never mask the
	// boundary by skipping a row for the wrong reason.
	t.Run("user soft-delete sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := boundaryCutoff()

		for i, deletedAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			user := SeedUser(t, st, "boundary-del-user-"+string(rune('a'+i)))
			require.NoError(t, st.Users().Delete(ctx, user.ID))
			require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, user.ID, deletedAt))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().HardDeleteUsersBefore(ctx, c)
		})
	})

	t.Run("stale pending email sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := boundaryCutoff()

		for i, expiresAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			user := SeedUser(t, st, "boundary-email-user-"+string(rune('a'+i)))
			expiry := expiresAt
			minted, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
				ID:                      user.ID,
				PendingEmail:            user.Username + "@example.com",
				PendingEmailToken:       "TOKEN1",
				PendingEmailExpiresAt:   &expiry,
				PendingEmailUnblockedAt: time.Now().UTC().Add(time.Minute),
				Now:                     time.Now().UTC(),
			})
			mustSetPendingEmail(t, minted, err)
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().ClearStalePendingEmails(ctx, c)
		})
	})

	t.Run("lifecycle outbox consumed-before sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-outbox-user")
		cutoff := boundaryCutoff()

		insert := func() int64 {
			require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
				UserID:  userid.MustNew(user.ID),
				OpType:  "create",
				Payload: []byte("payload"),
			}))
			pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
				UserID: userid.MustNew(user.ID),
				Limit:  100,
			})
			require.NoError(t, err)
			require.NotEmpty(t, pending)
			return pending[len(pending)-1].ID
		}
		for _, consumedAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			require.NoError(t, st.LifecycleOutbox().MarkConsumed(ctx, store.MarkLifecycleOutboxConsumedParams{
				ID:         insert(),
				ConsumedAt: consumedAt,
			}))
		}
		// Unconsumed rows must survive any cutoff (consumed_at IS NOT NULL filter).
		unconsumedID := insert()

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.LifecycleOutbox().DeleteConsumedBefore(ctx, c)
		})
		pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{UserID: userid.MustNew(user.ID), Limit: 100})
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, unconsumedID, pending[0].ID)
	})

	t.Run("recent batch id expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "boundary-user")
		cutoff := boundaryCutoff()

		insert := func(batchID string, expiresAt time.Time) {
			require.NoError(t, st.UserRecentBatchIDs().Insert(ctx, store.InsertUserRecentBatchIDParams{
				UserID:              userid.MustNew(user.ID),
				BatchID:             batchID,
				BodyHash:            []byte("hash"),
				PrincipalID:         user.ID,
				CanonicalPhysicalMs: 1,
				CanonicalLogical:    1,
				CanonicalClient:     "client",
				OpCount:             1,
				Epoch:               1,
				ExpiresAt:           expiresAt,
			}))
		}
		insert("batch-expired", cutoff.Add(-time.Millisecond))
		insert("batch-at-cutoff", cutoff)
		insert("batch-live", cutoff.Add(time.Millisecond))

		deleted, err := st.UserRecentBatchIDs().DeleteExpired(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)

		_, err = st.UserRecentBatchIDs().Get(ctx, userid.MustNew(user.ID), "batch-expired")
		assert.ErrorIs(t, err, store.ErrNotFound)
		for _, keep := range []string{"batch-at-cutoff", "batch-live"} {
			_, err := st.UserRecentBatchIDs().Get(ctx, userid.MustNew(user.ID), keep)
			assert.NoError(t, err)
		}
	})
}
