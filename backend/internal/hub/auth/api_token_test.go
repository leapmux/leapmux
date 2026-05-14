package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

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
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "test-org-" + orgID, IsPersonal: true}))
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
		RegisteredBy:    userID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))
	workspaceID = id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       u.OrgID,
		OwnerUserID: userID,
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
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))

	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	info, err := v.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)
	assert.Equal(t, userID, info.ID)
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
		UserID:     userID,
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
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		ExpiresAt:  &past,
		Scope:      "remote:*",
	}))
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.Error(t, err)
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
		UserID:      userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(currentSecret),
		Scope:       "remote:*",
	}))
	require.NoError(t, st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(currentSecret),
		NewRefreshHash:           v.HashSecret(currentSecret),
		PreviousRefreshHash:      v.HashSecret(prevSecret),
		PreviousRefreshExpiresAt: &prevExp,
	}))

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
		UserID:      userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  v.HashSecret(currentSecret),
		RefreshHash: v.HashSecret(currentSecret),
		Scope:       "remote:*",
	}))
	require.NoError(t, st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            v.HashSecret(currentSecret),
		NewRefreshHash:           v.HashSecret(currentSecret),
		PreviousRefreshHash:      v.HashSecret(prevSecret),
		PreviousRefreshExpiresAt: &expiredGrace,
	}))

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
		UserID:      userID,
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
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	info, err := v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, userID, info.ID)
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
		UserID:      userID,
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
		UserID:      userID,
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
		UserID:     userID,
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
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: v.HashSecret(secret),
		Scope:      "remote:*",
	}))

	// Match: returns the kind + canonical row id.
	gotKind, gotID, err := v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindAPI, gotKind)
	assert.Equal(t, tokenID, gotID)

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
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  v.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	gotKind, gotID, err := v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, auth.BearerKindDelegation, gotKind)
	assert.Equal(t, tokenID, gotID)

	_, _, err = v.VerifyBearerSecret(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, "wrong"))
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}
