package service

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestResolveIssuedScopesChecksTheClosedRequest pins where the issuer-ceiling
// check reads the scope set: the minted grant is the CLOSED set (Close() adds
// git:write's implied git:read and worker:read), so checking the unclosed
// request and closing afterwards let a mint gain implied scopes PAST the
// check that was supposed to bound it. An issuer whose reachable set is
// unclosed -- the hand-edited or restored row loadBearer's comments defend
// against -- must be refused, not accommodated.
func TestResolveIssuedScopesChecksTheClosedRequest(t *testing.T) {
	t.Parallel()

	owner := &store.User{ID: "u-1", Username: "owner", IsAdmin: true}
	// Parse does NOT close: this actor set lists git:write without the scopes
	// it implies, exactly what a hand-edited row produces.
	unclosed, err := authscope.Parse("git:write")
	require.NoError(t, err)
	require.False(t, unclosed.Allows(leapmuxv1.Scope_SCOPE_GIT_READ),
		"precondition: the actor set is genuinely unclosed")

	_, err = resolveIssuedScopes([]string{"git:write"}, owner, &auth.UserInfo{Scopes: unclosed})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"closing the request after the check let a mint reach scopes its issuer could not")

	// A CLOSED issuer set covering the closed request still passes, and the
	// stored grant comes back closed.
	closed := unclosed.Close()
	granted, err := resolveIssuedScopes([]string{"git:write"}, owner, &auth.UserInfo{Scopes: closed})
	require.NoError(t, err)
	assert.True(t, granted.Allows(leapmuxv1.Scope_SCOPE_GIT_READ),
		"the minted grant states the implications, as every stored set must")
}
