package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// These tests exercise delegation_token-specific behavior the plan
// called out beyond the basic accept/expire/revoke cases that
// api_token_test.go already covers:
//
//   - issued_for_tab_id is recorded on the row but not consulted by
//     the validator (it's provenance only — the workspace is the
//     authoritative scope);
//   - sibling-tab access works because validation is workspace-wide,
//     not tab-locked;
//   - tokens minted for different (user, workspace) pairs are
//     independent — revoking one doesn't void the others;
//   - `Touch` advances `last_used_at` so admin tooling can surface
//     a non-stale "in-use" indicator;
//   - the DeleteRevokedBefore / DeleteExpiredBefore cleanup queries
//     respect their cutoff and don't drop active rows.

const testPepper = "0123456789abcdef0123456789abcdef"

func newValidator(t *testing.T, st store.Store) *auth.TokenValidator {
	t.Helper()
	v, err := auth.NewTokenValidator(st, []byte(testPepper))
	require.NoError(t, err)
	return v
}

func mintDelegation(t *testing.T, st store.Store, v *auth.TokenValidator, p store.CreateDelegationTokenParams) (tokenID, secret string) {
	t.Helper()
	if p.ID == "" {
		p.ID = id.Generate()
	}
	if len(p.SecretHash) == 0 {
		secret = auth.MintAccessSecret()
		p.SecretHash = v.HashSecret(secret)
	}
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = time.Now().Add(time.Hour)
	}
	require.NoError(t, st.DelegationTokens().Create(context.Background(), p))
	return p.ID, secret
}

func TestDelegationToken_IssuedForTabIDIsProvenanceOnly(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	// Provenance fields recorded but not enforced. The validator
	// must ACCEPT the bearer regardless of whether the
	// issued_for_tab_id corresponds to a real tab — the row is for
	// audit, not authorization.
	tokenID, secret := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID:           userID,
		WorkerID:         workerID,
		WorkspaceID:      workspaceID,
		IssuedForTabID:   "no-such-tab",
		IssuedForTabType: 1,
		AgentID:          "agent-no-such",
	})

	info, err := v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)
	assert.Equal(t, userID, info.ID)

	row, err := st.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "no-such-tab", row.IssuedForTabID,
		"provenance field must round-trip through the store")
	assert.Equal(t, "agent-no-such", row.AgentID,
		"agent_id provenance must round-trip through the store")
}

func TestDelegationToken_DifferentWorkspacesAreIndependent(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceA := seedWorkerAndWorkspace(t, st, userID)

	// Second workspace owned by the same user. The fixture only
	// creates one; mint a sibling here so we can prove the validator
	// doesn't conflate them.
	workspaceB := id.Generate()
	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceB,
		OrgID:       user.OrgID,
		OwnerUserID: userID,
		Title:       "test-ws-b",
	}))

	tokenA, secretA := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID: userID, WorkerID: workerID, WorkspaceID: workspaceA,
	})
	tokenB, secretB := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID: userID, WorkerID: workerID, WorkspaceID: workspaceB,
	})

	// Both validate independently.
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenA, secretA))
	require.NoError(t, err)
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenB, secretB))
	require.NoError(t, err)

	// Revoking workspace A's token must not affect workspace B's.
	_, err = st.DelegationTokens().Revoke(context.Background(), tokenA)
	require.NoError(t, err)
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenA, secretA))
	require.Error(t, err)
	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenB, secretB))
	require.NoError(t, err, "revoking sibling token must not cascade")
}

func TestDelegationToken_TouchUpdatesLastUsedAt(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	tokenID, secret := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID: userID, WorkerID: workerID, WorkspaceID: workspaceID,
	})

	// Brand-new row: last_used_at is unset until validation runs.
	rowBefore, err := st.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, rowBefore.LastUsedAt,
		"freshly-minted token must have no LastUsedAt yet")

	_, err = v.ValidateBearer(context.Background(), auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	require.NoError(t, err)

	rowAfter, err := st.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	require.NotNil(t, rowAfter.LastUsedAt,
		"successful validation must populate LastUsedAt for admin/audit views")
}

func TestDelegationToken_DeleteRevokedBefore_RespectsCutoff(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	// Active (not revoked) token — must be left alone by cleanup.
	activeID, _ := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID: userID, WorkerID: workerID, WorkspaceID: workspaceID,
	})

	// Revoked token — should be hard-deleted when cleanup runs with a
	// cutoff after its revocation timestamp.
	revokedID, _ := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID: userID, WorkerID: workerID, WorkspaceID: workspaceID,
	})
	_, err := st.DelegationTokens().Revoke(context.Background(), revokedID)
	require.NoError(t, err)

	// Cutoff in the future: deletes the revoked row. The query uses
	// datetime() on both sides to normalize the strftime-written
	// revoked_at against Go's driver-bound cutoff — see the SQL file
	// for the rationale.
	deleted, err := st.DelegationTokens().DeleteRevokedBefore(context.Background(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1), "cleanup must delete the revoked row")

	// Active token still present; lookup succeeds.
	_, err = st.DelegationTokens().GetByID(context.Background(), activeID)
	require.NoError(t, err, "cleanup must not touch active rows")
	// Revoked token gone.
	_, err = st.DelegationTokens().GetByID(context.Background(), revokedID)
	require.Error(t, err, "revoked row must have been hard-deleted")
}

func TestDelegationToken_DeleteExpiredBefore_RespectsCutoff(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	// Long-lived token: must not be swept.
	freshID, _ := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		ExpiresAt:   time.Now().Add(time.Hour),
	})

	// Already-expired token: must be swept.
	expiredID, _ := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		ExpiresAt:   time.Now().Add(-time.Minute),
	})

	// Cutoff = now: only rows whose ExpiresAt < now get deleted.
	deleted, err := st.DelegationTokens().DeleteExpiredBefore(context.Background(), time.Now())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1), "cleanup must delete the expired row")

	_, err = st.DelegationTokens().GetByID(context.Background(), freshID)
	require.NoError(t, err, "fresh row must remain")
	_, err = st.DelegationTokens().GetByID(context.Background(), expiredID)
	require.Error(t, err, "expired row must be gone")
}

func TestDelegationToken_RotateRefreshAdvancesPreviousHash(t *testing.T) {
	st := newTestStore(t)
	v := newValidator(t, st)
	userID := seedUser(t, st)
	workerID, workspaceID := seedWorkerAndWorkspace(t, st, userID)

	tokenID, _ := mintDelegation(t, st, v, store.CreateDelegationTokenParams{
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		RefreshHash: v.HashSecret("orig-refresh"),
	})

	// Rotate: the new refresh becomes current; the old one moves to
	// previous_refresh_hash with a grace expiry.
	prevExp := time.Now().Add(auth.RefreshReuseGrace)
	require.NoError(t, st.DelegationTokens().RotateRefresh(context.Background(), store.RotateDelegationTokenRefreshParams{
		ID:                       tokenID,
		NewRefreshHash:           v.HashSecret("new-refresh"),
		PreviousRefreshHash:      v.HashSecret("orig-refresh"),
		PreviousRefreshExpiresAt: &prevExp,
	}))

	row, err := st.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	require.NotNil(t, row.PreviousRefreshExpiresAt, "previous_refresh_expires_at must be persisted on rotation")
	assert.WithinDuration(t, prevExp, *row.PreviousRefreshExpiresAt, time.Second,
		"previous_refresh_expires_at must round-trip through the store")
	// PreviousRefreshHash recorded.
	assert.NotEmpty(t, row.PreviousRefreshHash)
}
