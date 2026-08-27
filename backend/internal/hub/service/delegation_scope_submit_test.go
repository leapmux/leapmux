package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// submitWorkerBound composes the two rules that limit SetTabRegisterOp, and
// this is the matrix that proves neither can be taken without the other.
//
// SubmitOps writes the whole layout document, so workspace:write is its method
// scope. Binding a tab to a WORKER is the one thing inside that write which
// reaches a machine, and it answers to two rules at once: the delegation bound
// (a bearer minted by worker A must not point a tab at worker B) and worker:read
// (an app never granted it may reach no machine at all).
//
// The two rows where exactly ONE rule bites are the ones a split implementation
// gets wrong, so they are the reason this test exists.
//
// Which workers a bounded delegation scope admits is DelegationWorkerScope's own
// contract, covered in auth/delegation_worker_scope_test.go. This test uses its
// two poles and asks only what the composition does with them.
func TestSubmitWorkerBound_ComposesTheScopeAndTheDelegationBound(t *testing.T) {
	t.Parallel()

	const anyWorker = "worker-1"

	withScopes := func(set authscope.ScopeSet) *auth.UserInfo {
		return &auth.UserInfo{ID: userid.MustNew("u1"), Scopes: set}
	}
	unscoped := withScopes(authscope.UnscopedGrant())
	noWorkerRead := withScopes(authscope.MustNew(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE))
	withWorkerRead := withScopes(authscope.MustNew(
		leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE, leapmuxv1.Scope_SCOPE_WORKER_READ))

	for name, tc := range map[string]struct {
		user *auth.UserInfo
		// scope is one of the two poles: unbounded, or deny-all.
		scope auth.DelegationWorkerScope
		// wantNil says the caller carries NO worker narrowing. nil is how the
		// crdt package reads that; a non-nil always-true predicate would
		// install a decorator that constrains nothing, which is a different
		// thing and a wasted allocation on the hottest bearer RPC.
		wantNil     bool
		wantAllowed bool
	}{
		"neither rule bites": {
			user: unscoped, scope: auth.UnboundedScope(), wantNil: true,
		},
		"the scope grants worker:read and there is no delegation bound": {
			user: withWorkerRead, scope: auth.UnboundedScope(), wantNil: true,
		},
		"the delegation bound alone": {
			user: unscoped, scope: auth.DenyAllScope(), wantAllowed: false,
		},
		"the scope alone": {
			user: noWorkerRead, scope: auth.UnboundedScope(), wantAllowed: false,
		},
		"both": {
			user: noWorkerRead, scope: auth.DenyAllScope(), wantAllowed: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := submitWorkerBound(tc.user, tc.scope)
			if tc.wantNil {
				assert.Nil(t, got, "an unbounded caller must install no decorator")
				return
			}
			if !assert.NotNil(t, got, "a bounded caller must install a predicate") {
				return
			}
			assert.Equal(t, tc.wantAllowed, got(anyWorker))
		})
	}

	// A nil user reaches nothing, because the zero grant allows no scope. The
	// handler always holds one, so this is the fail-closed arm rather than a
	// reachable path -- but it must not be the UNBOUNDED arm.
	nilUser := submitWorkerBound(nil, auth.UnboundedScope())
	if assert.NotNil(t, nilUser, "an unattributable caller must not be unbounded") {
		assert.False(t, nilUser(anyWorker))
	}
}
