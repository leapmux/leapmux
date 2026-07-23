package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// listTabsOrgBinding holds the hub's only AnyOrg(), so each arm is pinned here.
//
// The delegation arm is the one that matters: a bearer is pinned to a single
// workspace that may live OUTSIDE the caller's home org, so binding to the home
// org would hide the caller's own pinned workspace. The non-delegation arms must
// NOT reach AnyOrg -- that would drop the org check for ordinary callers.
func TestListTabsOrgBinding(t *testing.T) {
	delegated := &auth.UserInfo{
		ID:         userid.MustNew("u-1"),
		OrgID:      "home-org",
		Credential: auth.DelegationCredential("tok", "pinned-ws", "minter"),
	}
	ordinary := &auth.UserInfo{
		ID:         userid.MustNew("u-1"),
		OrgID:      "home-org",
		Credential: auth.SessionCredential("sess-1"),
	}

	t.Run("explicit org wins for a delegation caller", func(t *testing.T) {
		got := listTabsOrgBinding("req-org", delegated)
		orgID, ok := got.ListFilterOrgID()
		assert.True(t, ok)
		assert.Equal(t, "req-org", orgID, "an explicit org_id binds even for a bearer")
	})

	t.Run("delegation with no org uses AnyOrg", func(t *testing.T) {
		got := listTabsOrgBinding("", delegated)
		// Pin the exact value rather than a !isBound/!DeniesAll pair: OrgBinding
		// is comparable, so this also rules out any state that is neither.
		assert.Equal(t, auth.AnyOrg(), got)
		assert.False(t, got.DeniesAll(), "AnyOrg must admit, not deny")
	})

	t.Run("ordinary caller with no org falls back to the home org", func(t *testing.T) {
		got := listTabsOrgBinding("", ordinary)
		orgID, ok := got.ListFilterOrgID()
		assert.True(t, ok, "an ordinary caller must stay bound to an org")
		assert.Equal(t, "home-org", orgID)
	})

	t.Run("ordinary caller with a blank home org denies", func(t *testing.T) {
		blankOrg := &auth.UserInfo{ID: userid.MustNew("u-2"), Credential: auth.SessionCredential("s")}
		got := listTabsOrgBinding("", blankOrg)
		assert.True(t, got.DeniesAll(),
			"a corrupt identity must fail closed, never widen to AnyOrg")
	})

	t.Run("nil user denies", func(t *testing.T) {
		assert.True(t, listTabsOrgBinding("", nil).DeniesAll())
	})

	// The nil-user refusal outranks the request's own org_id: a caller with no
	// identity must not get to name the scope it is checked against. Unreachable
	// through ListTabs today (MustGetUser fails first), which is exactly why it
	// is pinned -- the next entrypoint to reuse this helper inherits the deny.
	t.Run("nil user denies even with an explicit org", func(t *testing.T) {
		got := listTabsOrgBinding("req-org", nil)
		assert.True(t, got.DeniesAll(),
			"an unidentified caller must not bind the org it supplied itself")
		_, ok := got.ListFilterOrgID()
		assert.False(t, ok, "a deny must not surface a list filter")
	})
}
