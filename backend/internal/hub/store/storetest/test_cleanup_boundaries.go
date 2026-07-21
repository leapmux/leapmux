package storetest

import (
	"testing"
	"time"

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
// missed every same-day row on SQLite) ship green. Sweeps whose compared
// operand is not caller-controllable through the store API (the users/orgs
// hard-deletes, whose FK gating testCleanup covers) share the same compare
// shape and are represented here by the workspace/worker twins.
//
// Where a table has no unfiltered GetByID to enumerate survivors, a second
// sweep with a far-future cutoff pins the survivor count instead: it must
// delete exactly the rows the first sweep correctly kept.
func (s *Suite) testCleanupBoundaries(t *testing.T) {
	t.Run("delegation token expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "boundary-ws")
		cutoff := boundaryCutoff()

		create := func(expiresAt time.Time) string {
			tokenID := id.Generate()
			require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
				ID:          tokenID,
				UserID:      user.ID,
				WorkerID:    worker.ID,
				WorkspaceID: wsID,
				SecretHash:  []byte("secret"),
				ExpiresAt:   expiresAt,
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

	t.Run("revoked token sweeps are strict at the stored revoke instant", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "boundary-ws")

		// revoked_at is written DB-side (NOW/strftime), so read the stored
		// instant back and place cutoffs exactly on and just past it: the
		// sweep must be a strict < against the value the revoke path stored,
		// whatever the dialect's precision.
		apiID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:         apiID,
			UserID:     user.ID,
			ClientType: "cli",
			ClientName: "boundary-client",
			SecretHash: []byte("secret"),
			Scope:      "remote:*",
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
			ID:          delID,
			UserID:      user.ID,
			WorkerID:    worker.ID,
			WorkspaceID: wsID,
			SecretHash:  []byte("secret"),
			ExpiresAt:   boundaryCutoff().Add(time.Hour),
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
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
		cutoff := boundaryCutoff()

		for _, c := range []struct {
			code      string
			expiresAt time.Time
		}{
			{"code-expired", cutoff.Add(-time.Millisecond)},
			{"code-at-cutoff", cutoff},
			{"code-live", cutoff.Add(time.Millisecond)},
		} {
			require.NoError(t, st.CLIAuthorizationCodes().Create(ctx, store.CreateCLIAuthorizationCodeParams{
				Code:          c.code,
				UserID:        user.ID,
				CodeChallenge: "challenge",
				ExpiresAt:     c.expiresAt,
			}))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().DeleteExpiredCLIAuthorizationCodes(ctx, c)
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
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
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
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
		cutoff := boundaryCutoff()

		for i, deletedAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			wsID := SeedWorkspace(t, st, orgID, user.ID, "boundary-ws")
			_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
				ID:          wsID,
				OwnerUserID: user.ID,
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
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
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

	t.Run("stale pending email sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "boundary-org")
		cutoff := boundaryCutoff()

		for i, expiresAt := range []time.Time{
			cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
		} {
			user := SeedUser(t, st, orgID, "boundary-email-user-"+string(rune('a'+i)))
			expiry := expiresAt
			require.NoError(t, st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
				ID:                    user.ID,
				PendingEmail:          user.Username + "@example.com",
				PendingEmailToken:     "TOKEN1",
				PendingEmailExpiresAt: &expiry,
			}))
		}

		assertBoundarySweep(t, cutoff, func(c time.Time) (int64, error) {
			return st.Cleanup().ClearStalePendingEmails(ctx, c)
		})
	})

	t.Run("lifecycle outbox consumed-before sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "boundary-org")
		cutoff := boundaryCutoff()

		insert := func() int64 {
			require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
				OrgID:   orgID,
				OpType:  "create",
				Payload: []byte("payload"),
			}))
			pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
				OrgID: orgID,
				Limit: 100,
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
		pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{OrgID: orgID, Limit: 100})
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, unconsumedID, pending[0].ID)
	})

	t.Run("recent batch id expiry sweep is millisecond-exact", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "boundary-org")
		user := SeedUser(t, st, orgID, "boundary-user")
		cutoff := boundaryCutoff()

		insert := func(batchID string, expiresAt time.Time) {
			require.NoError(t, st.OrgRecentBatchIDs().Insert(ctx, store.InsertOrgRecentBatchIDParams{
				OrgID:               orgID,
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

		deleted, err := st.OrgRecentBatchIDs().DeleteExpired(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)

		_, err = st.OrgRecentBatchIDs().Get(ctx, orgID, "batch-expired")
		assert.ErrorIs(t, err, store.ErrNotFound)
		for _, keep := range []string{"batch-at-cutoff", "batch-live"} {
			_, err := st.OrgRecentBatchIDs().Get(ctx, orgID, keep)
			assert.NoError(t, err)
		}
	})
}
