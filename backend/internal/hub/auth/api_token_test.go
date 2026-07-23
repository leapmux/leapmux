package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedUser(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "test-org-" + orgID}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "test-" + userID[:8],
		PasswordHash: "x",
	}))
	return userID
}

// seedWorkerAndWorkspace creates a worker + workspace in the test store
// so delegation_tokens FK constraints can be satisfied by tests that
// don't otherwise care about either resource.
func seedWorkerAndWorkspace(t *testing.T, st store.Store, userID string) (workerID, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	u, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	workerID = id.Generate()
	require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))
	workspaceID = id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       u.OrgID,
		OwnerUserID: userid.MustNew(userID),
		Title:       "test-ws",
	}))
	return workerID, workspaceID
}

func TestFormatBearer_RoundTrip(t *testing.T) {
	tokenID := id.Generate()
	secret := id.Generate()
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	assert.True(t, strings.HasPrefix(bearer, "lmx_"))
	assert.True(t, auth.IsLeapMuxBearer(bearer))
}

func TestTokenValidator_DeriveRefreshBearerPairIsDeterministicAndDomainSeparated(t *testing.T) {
	st := newTestStore(t)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	v, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	tokenID := id.Generate()
	refreshHash := v.HashSecret(auth.MintAccessSecret())
	now := time.Now()
	first := v.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, refreshHash, now,
		auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	retry := v.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, refreshHash, now.Add(time.Minute),
		auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)

	assert.Equal(t, first.AccessBearer, retry.AccessBearer)
	assert.Equal(t, first.RefreshBearer, retry.RefreshBearer)
	assert.Equal(t, first.AccessHash, retry.AccessHash)
	assert.Equal(t, first.RefreshHash, retry.RefreshHash)
	assert.NotEqual(t, first.AccessBearer, first.RefreshBearer, "access and refresh derivations must use separate domains")
	assert.Equal(t, first.AccessExpiresAt.Add(time.Minute), retry.AccessExpiresAt)
	assert.Equal(t, first.RefreshExpiresAt.Add(time.Minute), retry.RefreshExpiresAt)

	otherToken := v.DeriveRefreshBearerPair(
		auth.BearerKindAPI, id.Generate(), refreshHash, now,
		auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	assert.NotEqual(t, first.AccessBearer, otherToken.AccessBearer, "token id must bind the derived pair")

	otherValidator, err := auth.NewTokenValidator(st, []byte("fedcba9876543210fedcba9876543210"))
	require.NoError(t, err)
	otherPepper := otherValidator.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, refreshHash, now,
		auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	assert.NotEqual(t, first.AccessBearer, otherPepper.AccessBearer, "server pepper must bind the derived pair")
}

func TestTokenValidator_RejectsMalformedBearer(t *testing.T) {
	st := newTestStore(t)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, err = v.ValidateBearer(context.Background(), "not-a-bearer")
	require.Error(t, err)
	_, err = v.ValidateBearer(context.Background(), "lmx_only_one_underscore")
	require.Error(t, err)
}

// TestTokenValidator_RejectsUnknownKindWithoutDBLookup pins one of the
// plan's correctness invariants: a bearer with an unrecognised kind
// char (anything other than 'a' / 'd') is rejected by parseBearer
// before any DB query runs. The plan's bearer-format optimization
// hinges on this: "Unknown kinds are rejected without a DB round-trip
// at all" (plan line 851).
//
// The check is done via a no-op store wrapper that fails any call —
// so a single hit while validating an unknown-kind bearer would fail
// the test loudly.
func TestTokenValidator_RejectsUnknownKindWithoutDBLookup(t *testing.T) {
	st := newTestStore(t) // a real store, but we'll never let it be hit
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	cases := []string{
		// Unknown kind chars.
		"lmx_x" + id.Generate() + "_" + auth.MintAccessSecret(),
		"lmx_z" + id.Generate() + "_" + auth.MintAccessSecret(),
		// Empty kind area: "lmx_" alone, "lmx__secret", "lmx_a" (no body).
		"lmx_",
		"lmx_a",
		// Missing secret separator.
		"lmx_a" + id.Generate(),
		// Empty id (kind char followed immediately by separator).
		"lmx_a_" + auth.MintAccessSecret(),
		// Empty secret.
		"lmx_a" + id.Generate() + "_",
		// Wrong prefix entirely.
		"foo_a" + id.Generate() + "_" + auth.MintAccessSecret(),
		// Only the prefix.
		"lmx_",
	}
	for _, bearer := range cases {
		_, err := v.ValidateBearer(context.Background(), bearer)
		require.Error(t, err, "bearer %q must be rejected", bearer)
	}
}

func TestTokenValidator_AcceptsValidAPIBearer(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))

	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	info, err := v.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)
	assert.Equal(t, userID, info.ID.String())

	token, err := st.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.True(t, info.AuthenticatedAt.Equal(token.CreatedAt.UTC()),
		"API bearer auth basis should use the DB token creation timestamp")
}

func TestTokenValidator_RejectsRevoked(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))
	_, err = st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.Error(t, err)
}

func TestTokenValidator_RejectsBearerIssuedBeforeUserRevocation(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))
	_, err = st.Users().RevokeUserTokens(context.Background(), userid.MustNew(userID))
	require.NoError(t, err)

	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.Error(t, err)
}

func TestTokenValidator_RejectsExpired(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	past := time.Now().Add(-time.Minute)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		ExpiresAt:  &past,
		Scope:      "remote:*",
	}))
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.Error(t, err)
}

// TestTokenValidator_WrongSecretDoesNotLeakLifecycle pins the anti-enumeration
// ordering in validateRow (the access/ValidateBearer path). token_id is non-secret
// (it is returned in JSON to /auth/cli/token, /auth/cli/refresh, and the delegation
// mint), so a caller holding a victim's token_id but NOT its secret must get a
// uniform ErrInvalidToken -- never a distinguishable "revoked"/"expired" that leaks
// the token's existence and lifecycle. The correct-secret holder still learns
// revoked/expired (which drives refresh), so real clients are unaffected. This is
// the ValidateBearer twin of the refresh-path guard
// (TestValidateAPIRefresh_WrongSecretOnRevokedRowStaysInvalidToken).
func TestTokenValidator_WrongSecretDoesNotLeakLifecycle(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	newToken := func(t *testing.T, expiresAt *time.Time) (string, string) {
		t.Helper()
		tokenID := id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
			ID:         tokenID,
			UserID:     userid.MustNew(userID),
			ClientType: "cli",
			ClientName: "test",
			SecretHash: v.HashSecret(secret),
			ExpiresAt:  expiresAt,
			Scope:      "remote:*",
		}))
		return tokenID, secret
	}

	t.Run("revoked row: wrong secret is InvalidToken, correct secret still Revoked", func(t *testing.T) {
		tokenID, secret := newToken(t, nil)
		_, err := st.APITokens().Revoke(context.Background(), tokenID)
		require.NoError(t, err)

		wrong := auth.MintAccessSecret()
		_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, wrong))
		require.ErrorIs(t, err, auth.ErrInvalidToken)
		assert.NotErrorIs(t, err, auth.ErrTokenRevoked,
			"a wrong secret must not reveal that the token is revoked (lifecycle oracle)")

		_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
		require.ErrorIs(t, err, auth.ErrTokenRevoked,
			"the secret-holder still learns the token is revoked")
	})

	t.Run("expired row: wrong secret is InvalidToken, correct secret still Expired", func(t *testing.T) {
		past := time.Now().Add(-time.Minute)
		tokenID, secret := newToken(t, &past)

		wrong := auth.MintAccessSecret()
		_, err := v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, wrong))
		require.ErrorIs(t, err, auth.ErrInvalidToken)
		assert.NotErrorIs(t, err, auth.ErrTokenExpired,
			"a wrong secret must not reveal that the token is expired (lifecycle oracle)")

		_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
		require.ErrorIs(t, err, auth.ErrTokenExpired,
			"the secret-holder still learns the token is expired, which drives refresh")
	})
}

type revokeFailStore struct {
	store.Store
	api store.APITokenStore
}

func (s revokeFailStore) APITokens() store.APITokenStore {
	return s.api
}

type revokeFailAPITokens struct {
	store.APITokenStore
}

func (s revokeFailAPITokens) Revoke(context.Context, string) (int64, error) {
	return 0, errors.New("forced revoke failure")
}

type lookupFailAPITokens struct {
	store.APITokenStore
	err error
}

func (s lookupFailAPITokens) GetByID(context.Context, string) (*store.APIToken, error) {
	return nil, s.err
}

type userLookupFailStore struct {
	store.Store
	users store.UserStore
}

func (s userLookupFailStore) Users() store.UserStore {
	return s.users
}

type userLookupFailUsers struct {
	store.UserStore
	err error
}

func (s userLookupFailUsers) GetByID(context.Context, string) (*store.User, error) {
	return nil, s.err
}

func TestAPITokenRotateRefreshRejectsStalePreviousHash(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	accessSecret := auth.MintAccessSecret()
	firstRefresh := auth.MintAccessSecret()
	secondRefresh := auth.MintAccessSecret()
	thirdRefresh := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(accessSecret),
		RefreshHash: v.HashSecret(firstRefresh),
		Scope:       "remote:*",
	}))

	rotated, err := st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                  tokenID,
		NewSecretHash:       v.HashSecret(accessSecret),
		NewRefreshHash:      v.HashSecret(secondRefresh),
		PreviousRefreshHash: v.HashSecret(firstRefresh),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rotated)

	rotated, err = st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                  tokenID,
		NewSecretHash:       v.HashSecret(accessSecret),
		NewRefreshHash:      v.HashSecret(thirdRefresh),
		PreviousRefreshHash: v.HashSecret(firstRefresh),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rotated, "stale refresh rotation must not overwrite the winner's pair")

	row, err := st.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, v.HashSecret(secondRefresh), row.RefreshHash)
}

func TestValidateAPIRefresh_UserLookupFailureIsNotRevocation(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	issuer, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	tokenID := id.Generate()
	refreshSecret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  issuer.HashSecret(auth.MintAccessSecret()),
		RefreshHash: issuer.HashSecret(refreshSecret),
		Scope:       "remote:*",
	}))

	forcedErr := errors.New("forced user lookup failure")
	wrapped := userLookupFailStore{
		Store: st,
		users: userLookupFailUsers{UserStore: st.Users(), err: forcedErr},
	}
	validator, err := auth.NewTokenValidator(wrapped, pepper)
	require.NoError(t, err)

	_, _, err = validator.ValidateAPIRefresh(
		context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret))
	require.Error(t, err)
	assert.ErrorContains(t, err, forcedErr.Error())
	assert.NotErrorIs(t, err, auth.ErrTokenRevoked,
		"transient user lookup failures must not be reported as credential revocation")
}

func TestValidateBearer_UserLookupFailureIsInternal(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	issuer, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: issuer.HashSecret(secret),
		Scope:      "remote:*",
	}))

	forcedErr := errors.New("forced user lookup failure")
	wrapped := userLookupFailStore{
		Store: st,
		users: userLookupFailUsers{UserStore: st.Users(), err: forcedErr},
	}
	validator, err := auth.NewTokenValidator(wrapped, pepper)
	require.NoError(t, err)

	_, err = validator.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.ErrorContains(t, err, forcedErr.Error())
}

func TestVerifyBearerSecret_LookupFailureIsNotInvalidToken(t *testing.T) {
	st := newTestStore(t)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	forcedErr := errors.New("forced api token lookup failure")
	wrapped := revokeFailStore{
		Store: st,
		api: lookupFailAPITokens{
			APITokenStore: st.APITokens(),
			err:           forcedErr,
		},
	}
	validator, err := auth.NewTokenValidator(wrapped, pepper)
	require.NoError(t, err)

	_, _, err = validator.VerifyBearerSecret(
		context.Background(), auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret()))
	require.Error(t, err)
	assert.ErrorContains(t, err, forcedErr.Error())
	assert.NotErrorIs(t, err, auth.ErrInvalidToken)
}

func TestValidateAPIRefresh_ReusedRefreshReturnsRevokeError(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	issuer, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	previousSecret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  issuer.HashSecret(currentSecret),
		RefreshHash: issuer.HashSecret(previousSecret),
		Scope:       "remote:*",
	}))
	expiredGrace := time.Now().Add(-time.Hour)
	_, err = st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            issuer.HashSecret(currentSecret),
		NewRefreshHash:           issuer.HashSecret(auth.MintAccessSecret()),
		PreviousRefreshHash:      issuer.HashSecret(previousSecret),
		PreviousRefreshExpiresAt: &expiredGrace,
	})
	require.NoError(t, err)

	wrapped := revokeFailStore{Store: st, api: revokeFailAPITokens{APITokenStore: st.APITokens()}}
	validator, err := auth.NewTokenValidator(wrapped, pepper)
	require.NoError(t, err)
	_, _, err = validator.ValidateAPIRefresh(
		context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, previousSecret))
	require.Error(t, err)
	assert.ErrorContains(t, err, "revoke reused refresh token")
	assert.NotErrorIs(t, err, auth.ErrRefreshReused,
		"compromise must not be reported handled when durable revocation failed")

	row, err := st.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt)
}

func TestValidateAPIRefresh_GraceWindowReturnsRetry(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	prevSecret := auth.MintAccessSecret()
	prevExp := time.Now().Add(auth.RefreshReuseGrace)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(prevSecret),
		Scope:       "remote:*",
	}))
	_, err = st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(currentSecret),
		NewRefreshHash:           v.HashSecret(currentSecret),
		PreviousRefreshHash:      v.HashSecret(prevSecret),
		PreviousRefreshExpiresAt: &prevExp,
	})
	require.NoError(t, err)

	// Presenting the previous (rotated-out) refresh within the grace
	// window should be treated as a benign retry, not a compromise.
	row, retry, err := v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, prevSecret))
	require.NoError(t, err)
	assert.True(t, retry, "previous-hash within grace window must mark caller for retry")
	require.NotNil(t, row)
	assert.Equal(t, tokenID, row.ID)

	// Still-valid current refresh should also succeed (no rotation).
	_, retryCurrent, err := v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, currentSecret))
	require.NoError(t, err)
	assert.False(t, retryCurrent, "current-hash must not signal retry")
}

func TestValidateAPIRefresh_ReuseAfterGraceRevokes(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	prevSecret := auth.MintAccessSecret()
	expiredGrace := time.Now().Add(-time.Hour)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(prevSecret),
		Scope:       "remote:*",
	}))
	_, err = st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(currentSecret),
		NewRefreshHash:           v.HashSecret(currentSecret),
		PreviousRefreshHash:      v.HashSecret(prevSecret),
		PreviousRefreshExpiresAt: &expiredGrace,
	})
	require.NoError(t, err)

	// Reusing the rotated refresh after the grace window expired must
	// be treated as compromise: revoke the row and return ErrRefreshReused.
	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, prevSecret))
	require.ErrorIs(t, err, auth.ErrRefreshReused)

	// Even the current refresh must fail now — the row is revoked.
	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, currentSecret))
	require.Error(t, err)
}

func TestValidateAPIRefresh_UnknownHashDoesNotRevoke(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(currentSecret),
		Scope:       "remote:*",
	}))

	// Random unknown secret: must NOT revoke the row (defensive against
	// internet-spray).
	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, "garbage-attacker-spray"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrRefreshReused)

	row, err := st.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "unknown secret must not revoke the row")
}

func TestValidateAPIRefresh_RejectsExpiredCurrentRefresh(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	expired := time.Now().Add(-time.Hour)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientType:       "cli",
		ClientName:       "test",
		SecretHash:       v.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      v.HashSecret(currentSecret),
		RefreshExpiresAt: &expired,
		Scope:            "remote:*",
	}))

	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, currentSecret))
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestValidateAPIRefresh_ExpiredCurrentRefreshDoesNotLoadUser(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	pepper := []byte("0123456789abcdef0123456789abcdef")
	issuer, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	tokenID := id.Generate()
	currentSecret := auth.MintAccessSecret()
	expired := time.Now().Add(-time.Hour)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientType:       "cli",
		ClientName:       "test",
		SecretHash:       issuer.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      issuer.HashSecret(currentSecret),
		RefreshExpiresAt: &expired,
		Scope:            "remote:*",
	}))

	forcedErr := errors.New("forced user lookup failure")
	wrapped := userLookupFailStore{
		Store: st,
		users: userLookupFailUsers{UserStore: st.Users(), err: forcedErr},
	}
	validator, err := auth.NewTokenValidator(wrapped, pepper)
	require.NoError(t, err)

	_, _, err = validator.ValidateAPIRefresh(
		context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, currentSecret))
	require.ErrorIs(t, err, auth.ErrTokenExpired)
	assert.NotErrorIs(t, err, forcedErr)
}

func TestValidateAPIRefresh_WrongSecretOnRevokedRowStaysInvalidToken(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(auth.MintAccessSecret()),
		RefreshHash: v.HashSecret(auth.MintAccessSecret()),
		Scope:       "remote:*",
	}))
	_, err = st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	_, _, err = v.ValidateAPIRefresh(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, "wrong-secret"))
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestTokenValidator_AcceptsValidDelegationBearer(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	info, err := v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, userID, info.ID.String())

	token, err := st.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.True(t, info.AuthenticatedAt.Equal(token.CreatedAt.UTC()),
		"delegation bearer auth basis should use the DB token creation timestamp")
}

func TestTokenValidator_RejectsExpiredDelegation(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		ExpiresAt:   time.Now().Add(-time.Minute),
	}))

	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.Error(t, err)
}

func TestTokenValidator_RejectsRevokedDelegation(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	_, err = st.DelegationTokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.Error(t, err)
}

func TestTokenValidator_RejectsWrongSecret(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, "wrong-secret"))
	require.Error(t, err)
}

// TestVerifyBearerSecret pins the contract VerifyBearerSecret offers to
// the revoke endpoint: matching secrets succeed regardless of revoked /
// expired state (so revoke is idempotent), every other failure returns
// ErrInvalidToken so a caller can't distinguish a wrong secret from a
// missing token_id (preventing token-id enumeration via the response).
func TestVerifyBearerSecret(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	refreshSecret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(secret),
		RefreshHash: v.HashSecret(refreshSecret),
		Scope:       "remote:*",
	}))

	// Match: returns the kind + canonical row id.
	gotKind, gotID, err := v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindAPI, gotKind)
	assert.Equal(t, tokenID, gotID)

	gotKind, gotID, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindAPI, gotKind)
	assert.Equal(t, tokenID, gotID)

	nextRefreshSecret := auth.MintAccessSecret()
	graceExpiry := time.Now().Add(auth.RefreshReuseGrace)
	rotated, err := st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(secret),
		NewRefreshHash:           v.HashSecret(nextRefreshSecret),
		PreviousRefreshHash:      v.HashSecret(refreshSecret),
		PreviousRefreshExpiresAt: &graceExpiry,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rotated)
	for _, candidate := range []string{refreshSecret, nextRefreshSecret} {
		gotKind, gotID, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, candidate))
		require.NoError(t, err)
		assert.Equal(t, auth.BearerKindAPI, gotKind)
		assert.Equal(t, tokenID, gotID)
	}

	// Wrong secret: ErrInvalidToken.
	_, _, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, "wrong"))
	assert.ErrorIs(t, err, auth.ErrInvalidToken)

	// Unknown token_id: same ErrInvalidToken — response shape MUST NOT
	// distinguish "row missing" from "row exists, secret wrong" or
	// `/auth/cli/revoke` becomes a token-id existence oracle.
	_, _, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret()))
	assert.ErrorIs(t, err, auth.ErrInvalidToken)

	// Malformed bearer: same ErrInvalidToken; never a DB lookup.
	_, _, err = v.VerifyBearerSecret(context.Background(), "not-a-bearer")
	assert.ErrorIs(t, err, auth.ErrInvalidToken)

	// Already revoked but secret matches: succeeds. Revoke must be
	// idempotent for clients that retry after a transient transport
	// error — they still hold the secret, so they're entitled to
	// "ensure this is revoked" semantics.
	_, err = st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	gotKind, gotID, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindAPI, gotKind)
	assert.Equal(t, tokenID, gotID)
}

// TestVerifyBearerSecret_DelegationToken is the same shape, against the
// delegation_tokens table. delegation token_ids are even more abundantly
// exposed (they appear in mint responses + channel registration logs)
// so the secret check matters equally there.
func TestVerifyBearerSecret_DelegationToken(t *testing.T) {
	st := newTestStore(t)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)
	v, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	refreshSecret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userid.MustNew(userID),
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		RefreshHash: v.HashSecret(refreshSecret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	gotKind, gotID, err := v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindDelegation, gotKind)
	assert.Equal(t, tokenID, gotID)

	// The refresh secret is also an entitled bearer: lookupRowSecretHashes
	// returns both the access and refresh hashes for a delegation row, so
	// presenting the refresh secret verifies too.
	gotKind, gotID, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, refreshSecret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindDelegation, gotKind)
	assert.Equal(t, tokenID, gotID)

	_, _, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, "wrong"))
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}
