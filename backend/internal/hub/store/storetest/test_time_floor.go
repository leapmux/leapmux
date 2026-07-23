package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// floorProbeResidue is the sub-millisecond tail carried by every bound instant
// in this group. The 750us half is deliberately >= 500us: a dialect that
// ROUNDS sub-millisecond fractions to its column precision (MySQL/TiDB
// DATETIME(3) round half up; SQLite's strftime '%f' does too before its
// Go-side floor) would store the NEXT millisecond and fail the "never after
// the bound" pin. The 999ns tail additionally exercises the nanosecond ->
// microsecond conversion on dialects that keep microsecond precision
// (pgx floors it for Postgres-family timestamptz columns).
const floorProbeResidue = 750*time.Microsecond + 999*time.Nanosecond

// floorProbe returns a future instant i whole milliseconds past a common
// base, carrying floorProbeResidue and expressed in boundaryZone (non-UTC, so
// offset normalization stays pinned too). Future-dated so liveness-guarded
// readbacks (GetActive, Extend) see the row as live.
func floorProbe(base time.Time, i int) time.Time {
	return base.Add(time.Duration(i)*time.Millisecond + floorProbeResidue).In(boundaryZone)
}

func floorProbeBase() time.Time {
	return time.Now().Add(time.Hour).Truncate(time.Millisecond)
}

// assertStoredInstant pins the round-trip contract for a Go-bound instant:
// the stored value must land inside [bound floored to ms, bound]. "Never
// after the bound" is the load-bearing half -- a stored deadline that
// postdates the minted one overclaims remaining lifetime (a ceil-to-seconds
// report gains a whole second, see the api-token refresh CAS test). "Never
// below the ms floor" rejects coarser truncation (e.g. to whole seconds).
func assertStoredInstant(t *testing.T, label string, bound, stored time.Time) {
	t.Helper()
	floor := bound.Truncate(time.Millisecond)
	assert.False(t, stored.After(bound),
		"%s: stored %s postdates its bound %s -- the sub-ms fraction was rounded up",
		label, stored.UTC().Format(time.RFC3339Nano), bound.UTC().Format(time.RFC3339Nano))
	assert.False(t, stored.Before(floor),
		"%s: stored %s predates the bound's ms floor %s -- precision coarser than a millisecond",
		label, stored.UTC().Format(time.RFC3339Nano), floor.UTC().Format(time.RFC3339Nano))
}

// testTimeFloor pins that every dialect FLOORS Go-bound instants to its
// column precision instead of rounding: reading a just-written deadline back
// must never yield a later instant than the one the caller minted. SQLite
// enforces this Go-side (sqltime.SQLiteTime/SQLiteNullTime.Value floor, because
// strftime '%f' rounds), MySQL/TiDB likewise Go-side (sqltime.MySQLTime, because
// the server rounds DATETIME(3) inserts half-up), and Postgres-family dialects
// via pgtime.Time (pgx floors nanoseconds to timestamptz microseconds). Each subtest drives a
// deadline-bearing write path through the store API with sub-millisecond
// probes and pins the readback.
func (s *Suite) testTimeFloor(t *testing.T) {
	t.Run("delegation token create floors bound expiries", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "floor-ws")
		base := floorProbeBase()

		tokenID := id.Generate()
		expiresAt := floorProbe(base, 0)
		refreshExpiresAt := floorProbe(base, 1)
		require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			ID:               tokenID,
			UserID:           userid.MustNew(user.ID),
			WorkerID:         worker.ID,
			WorkspaceID:      wsID,
			SecretHash:       []byte("secret"),
			ExpiresAt:        expiresAt,
			RefreshExpiresAt: &refreshExpiresAt,
		}))

		tok, err := st.DelegationTokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, tok.ExpiresAt)
		require.NotNil(t, tok.RefreshExpiresAt)
		assertStoredInstant(t, "refresh_expires_at", refreshExpiresAt, *tok.RefreshExpiresAt)
	})

	t.Run("api token create floors bound expiries", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")
		base := floorProbeBase()

		tokenID := id.Generate()
		expiresAt := floorProbe(base, 0)
		refreshExpiresAt := floorProbe(base, 1)
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           userid.MustNew(user.ID),
			ClientType:       "cli",
			ClientName:       "floor-client",
			SecretHash:       []byte("secret"),
			Scope:            "remote:*",
			ExpiresAt:        &expiresAt,
			RefreshExpiresAt: &refreshExpiresAt,
		}))

		tok, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.NotNil(t, tok.ExpiresAt)
		assertStoredInstant(t, "expires_at", expiresAt, *tok.ExpiresAt)
		require.NotNil(t, tok.RefreshExpiresAt)
		assertStoredInstant(t, "refresh_expires_at", refreshExpiresAt, *tok.RefreshExpiresAt)
	})

	t.Run("session create and touch floor bound instants", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")
		base := floorProbeBase()

		sessionID := id.Generate()
		expiresAt := floorProbe(base, 0)
		require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessionID,
			UserID:    userid.MustNew(user.ID),
			ExpiresAt: expiresAt,
		}))
		sess, err := st.Sessions().GetByID(ctx, sessionID)
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, sess.ExpiresAt)

		// Touch stamps last_active_at DB-side (NOW); the LastActiveAt param is
		// only the throttle threshold in the WHERE, so expires_at is the one
		// caller-bound instant that round-trips. A future threshold guarantees
		// the conditional update matches the just-created row.
		touchedExpiresAt := floorProbe(base, 2)
		n, err := st.Sessions().Touch(ctx, store.TouchSessionParams{
			ID:           sessionID,
			ExpiresAt:    touchedExpiresAt,
			LastActiveAt: floorProbe(base, 1),
		})
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		sess, err = st.Sessions().GetByID(ctx, sessionID)
		require.NoError(t, err)
		assertStoredInstant(t, "touched expires_at", touchedExpiresAt, sess.ExpiresAt)
	})

	t.Run("registration key create and extend floor bound expiry", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")
		base := floorProbeBase()

		expiresAt := floorProbe(base, 0)
		regID := SeedRegistrationKey(t, st, user.ID, expiresAt)
		key, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, key.ExpiresAt)

		extendedExpiresAt := floorProbe(base, 1)
		n, err := st.RegistrationKeys().Extend(ctx, store.ExtendRegistrationKeyParams{
			ID:        regID,
			CreatedBy: userid.MustNew(user.ID),
			ExpiresAt: extendedExpiresAt,
		})
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		key, err = st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assertStoredInstant(t, "extended expires_at", extendedExpiresAt, key.ExpiresAt)
	})

	t.Run("cli authorization code create floors bound expiry", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.CLIAuthorizationCodes().Create(ctx, store.CreateCLIAuthorizationCodeParams{
			Code:          "floor-code",
			UserID:        userid.MustNew(user.ID),
			CodeChallenge: "challenge",
			ExpiresAt:     expiresAt,
		}))

		code, err := st.CLIAuthorizationCodes().GetActive(ctx, "floor-code")
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, code.ExpiresAt)
	})

	t.Run("device authorization create floors bound expiry", func(t *testing.T) {
		st := s.NewStore(t)

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode:      "floor-device",
			UserCode:        "FLOOR-USER",
			IntervalSeconds: 5,
			ExpiresAt:       expiresAt,
		}))

		auth, err := st.DeviceAuthorizations().Get(ctx, "floor-device")
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, auth.ExpiresAt)
	})

	t.Run("org state upsert floors bound instants", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		base := floorProbeBase()

		epochStartedAt := floorProbe(base, 0)
		updatedAt := floorProbe(base, 1)
		require.NoError(t, st.OrgState().Upsert(ctx, store.UpsertOrgStateParams{
			OrgID:          orgID,
			StatePayload:   []byte("payload"),
			CurrentEpoch:   1,
			EpochStartedAt: epochStartedAt,
			UpdatedAt:      updatedAt,
		}))

		row, err := st.OrgState().Get(ctx, orgID)
		require.NoError(t, err)
		assertStoredInstant(t, "epoch_started_at", epochStartedAt, row.EpochStartedAt)
		assertStoredInstant(t, "updated_at", updatedAt, row.UpdatedAt)
	})

	t.Run("pending email expiry floors bound instant", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "floor@example.com",
			PendingEmailToken:     "TOKEN1",
			PendingEmailExpiresAt: &expiresAt,
		}))

		got, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, got.PendingEmailExpiresAt)
		assertStoredInstant(t, "pending_email_expires_at", expiresAt, *got.PendingEmailExpiresAt)
	})

	t.Run("org recent batch id insert floors bound expiry", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.OrgRecentBatchIDs().Insert(ctx, store.InsertOrgRecentBatchIDParams{
			OrgID:               orgID,
			BatchID:             "floor-batch",
			BodyHash:            []byte("hash"),
			PrincipalID:         user.ID,
			CanonicalPhysicalMs: 1,
			CanonicalLogical:    0,
			CanonicalClient:     "floor-client",
			OpCount:             1,
			Epoch:               1,
			ExpiresAt:           expiresAt,
		}))

		row, err := st.OrgRecentBatchIDs().Get(ctx, orgID, "floor-batch")
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, row.ExpiresAt)
	})

	t.Run("oauth state create floors bound expiry", func(t *testing.T) {
		st := s.NewStore(t)
		prov := SeedOAuthProvider(t, st, "floor-provider")

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        "floor-state",
			ProviderID:   prov.ID,
			PkceVerifier: "verifier",
			ExpiresAt:    expiresAt,
		}))

		state, err := st.OAuthStates().Get(ctx, "floor-state")
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, state.ExpiresAt)
	})

	t.Run("oauth tokens upsert floors bound expiry", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "floor-org")
		user := SeedUser(t, st, orgID, "floor-user")
		prov := SeedOAuthProvider(t, st, "floor-provider")

		expiresAt := floorProbe(floorProbeBase(), 0)
		require.NoError(t, st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       userid.MustNew(user.ID),
			ProviderID:   prov.ID,
			AccessToken:  []byte("access"),
			RefreshToken: []byte("refresh"),
			TokenType:    "Bearer",
			ExpiresAt:    expiresAt,
			KeyVersion:   1,
		}))

		tok, err := st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     userid.MustNew(user.ID),
			ProviderID: prov.ID,
		})
		require.NoError(t, err)
		assertStoredInstant(t, "expires_at", expiresAt, tok.ExpiresAt)
	})

	t.Run("pending oauth signup create floors bound expiries", func(t *testing.T) {
		st := s.NewStore(t)
		prov := SeedOAuthProvider(t, st, "floor-provider")
		base := floorProbeBase()

		tokenExpiresAt := floorProbe(base, 0)
		expiresAt := floorProbe(base, 1)
		require.NoError(t, st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
			Token:           "floor-signup",
			ProviderID:      prov.ID,
			ProviderSubject: "subject",
			AccessToken:     []byte("access"),
			RefreshToken:    []byte("refresh"),
			TokenType:       "Bearer",
			TokenExpiresAt:  tokenExpiresAt,
			KeyVersion:      1,
			ExpiresAt:       expiresAt,
		}))

		signup, err := st.PendingOAuthSignups().Get(ctx, "floor-signup")
		require.NoError(t, err)
		assertStoredInstant(t, "token_expires_at", tokenExpiresAt, signup.TokenExpiresAt)
		assertStoredInstant(t, "expires_at", expiresAt, signup.ExpiresAt)
	})
}
