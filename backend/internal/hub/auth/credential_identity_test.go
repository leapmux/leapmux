package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialIdentityKindsAreMutuallyExclusive(t *testing.T) {
	session := SessionCredential("session-1")
	assert.Equal(t, "session-1", session.SessionID())
	_, _, bearer := session.Bearer()
	assert.False(t, bearer)
	assert.Empty(t, session.WorkerScopeID())

	api := APICredential("api-1")
	kind, tokenID, bearer := api.Bearer()
	require.True(t, bearer)
	assert.Equal(t, BearerKindAPI, kind)
	assert.Equal(t, "api-1", tokenID)
	assert.Empty(t, api.SessionID())
	assert.Empty(t, api.WorkerScopeID())

	delegation := DelegationCredential("delegation-1", "worker-mint")
	kind, tokenID, bearer = delegation.Bearer()
	require.True(t, bearer)
	assert.Equal(t, BearerKindDelegation, kind)
	assert.Equal(t, "delegation-1", tokenID)
	assert.Equal(t, "worker-mint", delegation.WorkerScopeID())
	assert.Empty(t, delegation.SessionID())
}

// The minter bounds where a token may be used, so it must reach the two
// comparisons that decide "same credential?" in exactly opposite ways. Rejecting
// an empty minter is TestCredentialIdentityRejectsIncompleteStoredCredentials'
// job; this pins where a *populated* one is allowed to matter.
func TestCredentialIdentityWorkerScope(t *testing.T) {
	a := DelegationCredential("token", "worker-1")
	b := DelegationCredential("token", "worker-2")

	// The minter is part of the whole-identity comparison, and is now the ONLY
	// scope in it: Matches backs userCanUseChannel, so two credentials differing
	// in their minter must not be interchangeable there.
	assert.False(t, a.Matches(b), "a differing minter is a differing identity")

	// ...but it must NOT reach the CRDT actor key. PrincipalKey is derived from
	// the bearer row alone, so one token stays one actor regardless of scope --
	// letting the minter in would silently split a single token into two actors.
	assert.Equal(t, a.PrincipalKey(), b.PrincipalKey(),
		"the CRDT actor key identifies the token row, not its scopes")
}

// BearerRef is the canonical bearer reverse-index key shared by the auth
// revocation ledger and the channel manager. It must key api_tokens and
// delegation_tokens rows distinctly (so the two tables don't share a revocation
// namespace) and must round-trip identically whether built from a credential or
// from NewBearerRef.
func TestCredentialIdentityBearerRef(t *testing.T) {
	session := SessionCredential("session-1")
	_, ok := session.BearerRef()
	assert.False(t, ok, "a session credential is not a bearer")

	apiRef, ok := APICredential("token").BearerRef()
	require.True(t, ok)
	assert.Equal(t, NewBearerRef(BearerKindAPI, "token"), apiRef)

	delegationRef, ok := DelegationCredential("token", "worker-mint").BearerRef()
	require.True(t, ok)
	assert.Equal(t, NewBearerRef(BearerKindDelegation, "token"), delegationRef,
		"the delegation minter scope is not part of the bearer-row key")

	// Same token id, different table kind -> distinct keys (usable as map keys).
	assert.NotEqual(t, apiRef, delegationRef,
		"api_tokens and delegation_tokens rows must not share a revocation namespace")
	index := map[BearerRef]string{apiRef: "api", delegationRef: "delegation"}
	assert.Equal(t, "api", index[NewBearerRef(BearerKindAPI, "token")])
	assert.Equal(t, "delegation", index[NewBearerRef(BearerKindDelegation, "token")])
}

func TestCredentialIdentityRejectsIncompleteStoredCredentials(t *testing.T) {
	assert.Panics(t, func() { SessionCredential("") })
	assert.Panics(t, func() { APICredential("") })
	assert.Panics(t, func() { DelegationCredential("", "worker-mint") })
	// The minter is as required as the token id: it is what bounds WHERE the token
	// may be used, and delegation_tokens.worker_id is NOT NULL, so an empty one means
	// a code path dropped it rather than a data case.
	assert.Panics(t, func() { DelegationCredential("token", "") })
}

func TestCredentialIdentityMatchesWholeIdentity(t *testing.T) {
	assert.True(t, APICredential("token").Matches(APICredential("token")))
	assert.False(t, APICredential("token").Matches(DelegationCredential("token", "worker-mint")))
	assert.False(t, DelegationCredential("token", "worker-1").Matches(
		DelegationCredential("token", "worker-2")))
}

func TestPrincipalKeyIsKindDistinct(t *testing.T) {
	assert.Equal(t, "session:s1", SessionCredential("s1").PrincipalKey())

	apiKey := APICredential("t").PrincipalKey()
	delegationKey := DelegationCredential("t", "worker-mint").PrincipalKey()
	assert.Equal(t, fmt.Sprintf("bearer:%02x:t", byte(BearerKindAPI)), apiKey)
	assert.Equal(t, fmt.Sprintf("bearer:%02x:t", byte(BearerKindDelegation)), delegationKey)
	// The same token id under different bearer kinds must not collapse to one
	// CRDT actor -- api_tokens.id and delegation_tokens.id share no namespace.
	assert.NotEqual(t, apiKey, delegationKey)

	// The zero (synthetic/solo) identity has no principal key.
	assert.Empty(t, CredentialIdentity{}.PrincipalKey())
}

func TestCredentialCurrentExclusiveUpperBound(t *testing.T) {
	now := time.Now()

	var nilUser *UserInfo
	assert.False(t, nilUser.CredentialCurrent(now), "a nil credential is not current")
	assert.True(t, (&UserInfo{}).CredentialCurrent(now), "a zero expiry never expires")

	assert.True(t, (&UserInfo{CredentialExpiresAt: DeadlineAt(now.Add(time.Minute))}).CredentialCurrent(now))
	assert.False(t, (&UserInfo{CredentialExpiresAt: DeadlineAt(now)}).CredentialCurrent(now),
		"expiry is an exclusive upper bound: now == expiry is expired")
	assert.False(t, (&UserInfo{CredentialExpiresAt: DeadlineAt(now.Add(-time.Minute))}).CredentialCurrent(now))
}
