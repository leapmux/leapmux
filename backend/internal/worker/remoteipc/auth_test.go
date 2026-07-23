package remoteipc_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
)

func TestTokenStore_RegisterLookup(t *testing.T) {
	store := remoteipc.NewTokenStore()
	info := remoteipc.TokenInfo{
		UserID:      userid.MustNew("u-1"),
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "agent-1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
	}
	store.Register("raw-token", info)

	got, err := store.Lookup("raw-token")
	require.NoError(t, err)
	assert.Equal(t, info.UserID, got.UserID)
	assert.Equal(t, info.WorkspaceID, got.WorkspaceID)
	assert.Equal(t, info.TabID, got.TabID)
	assert.Equal(t, info.TabType, got.TabType)
}

func TestTokenStore_LookupUnknownReturnsErr(t *testing.T) {
	store := remoteipc.NewTokenStore()
	_, err := store.Lookup("never-registered")
	require.Error(t, err)
	assert.True(t, errors.Is(err, remoteipc.ErrUnknownToken))
}

func TestTokenStore_RevokeRemoves(t *testing.T) {
	store := remoteipc.NewTokenStore()
	store.Register("t-1", remoteipc.TokenInfo{UserID: userid.MustNew("u-1")})

	_, err := store.Lookup("t-1")
	require.NoError(t, err)

	store.Revoke("t-1")

	_, err = store.Lookup("t-1")
	require.Error(t, err)
}

func TestTokenStore_SetDelegationTokenID(t *testing.T) {
	store := remoteipc.NewTokenStore()
	store.Register("t-1", remoteipc.TokenInfo{UserID: userid.MustNew("u-1")})
	store.SetDelegationTokenID("t-1", "del-token-123")

	got, err := store.Lookup("t-1")
	require.NoError(t, err)
	assert.Equal(t, "del-token-123", got.DelegationTokenID)
}

func TestTokenStore_RevokeIdempotent(t *testing.T) {
	store := remoteipc.NewTokenStore()
	store.Revoke("never-registered")
	store.Register("t-1", remoteipc.TokenInfo{UserID: userid.MustNew("u-1")})
	store.Revoke("t-1")
	store.Revoke("t-1")
}

func TestEnvVars_NoSessionCookieLeak(t *testing.T) {
	envs := remoteipc.EnvVars("unix:/tmp/sock", "raw-token", remoteipc.TokenInfo{
		UserID:      userid.MustNew("u-1"),
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "agent-1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	for _, e := range envs {
		// The plan's threat model explicitly forbids leaking session
		// cookies into spawned-agent env vars. Defensive assertion.
		assert.NotContains(t, e, "LEAPMUX_COOKIE", "spawn must never leak session cookie")
		assert.NotContains(t, e, "LEAPMUX_SESSION", "spawn must never leak session id")
	}
}

// TestTokenStore_DistinctTokensResolveDistinctly pins the basic
// TokenStore guarantee that Register + Lookup keyed on the same
// token string round-trips the registered TokenInfo, and that
// distinct tokens never alias to the same entry. The inputs
// intentionally share a long common middle so a content-aware
// hash that summarises bytes (instead of cryptographically hashing
// them) can't accidentally land both tokens on the same bucket.
func TestTokenStore_DistinctTokensResolveDistinctly(t *testing.T) {
	s := remoteipc.NewTokenStore()
	tokA := "A" + strings.Repeat("X", 32) + "A"
	tokB := "B" + strings.Repeat("X", 32) + "B"
	infoA := remoteipc.TokenInfo{UserID: userid.MustNew("user-A")}
	infoB := remoteipc.TokenInfo{UserID: userid.MustNew("user-B")}
	s.Register(tokA, infoA)
	s.Register(tokB, infoB)

	gotA, err := s.Lookup(tokA)
	require.NoError(t, err)
	assert.Equal(t, userid.MustNew("user-A"), gotA.UserID)

	gotB, err := s.Lookup(tokB)
	require.NoError(t, err)
	assert.Equal(t, userid.MustNew("user-B"), gotB.UserID, "distinct tokens must resolve to distinct TokenInfo")
}
