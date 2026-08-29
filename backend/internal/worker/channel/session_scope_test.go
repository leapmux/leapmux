package channel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// The wire field's two poles, read at the HANDSHAKE.
//
// granted_scopes carries what the opening credential holds, and the worker's
// reading of it is the whole enforcement point: the Hub relays encrypted bytes
// it cannot inspect, so a channel that opened with the wrong grant is
// unrecoverable from that moment.
//
// An EMPTY list is the zero grant, which reaches nothing. The open is REFUSED
// rather than served, because a channel on which every method denies is worse
// than no channel: the client would hold a working session and meet a
// permission error on every call, with nothing naming the cause.
func TestHandleOpen_RefusesAnAnnouncedGrantOfNothing(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManagerWith(t, 0, 0)
	_, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)

	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-empty-grant",
		UserId:           "user-1",
		HandshakePayload: msg1,
		// No GrantedScopes at all.
	})
	require.NotEmpty(t, resp.GetError(), "an open announcing no grant must be refused")
	assert.Empty(t, resp.GetHandshakePayload(), "a refused open completes no handshake")
}

// SCOPE_ALL is the explicit absence of a limit, and the worker must read it as
// one. A session carries it, and a session reaches every method.
//
// It is EXCLUSIVE: a list carrying it beside named scopes came from a producer
// that meant neither, so the unscoped reading wins and the rest is redundant by
// definition.
func TestHandleOpen_ReadsScopeAllAsUnscoped(t *testing.T) {
	t.Parallel()

	for name, wire := range map[string][]leapmuxv1.Scope{
		"alone":                {leapmuxv1.Scope_SCOPE_ALL},
		"beside a named scope": {leapmuxv1.Scope_SCOPE_ALL, leapmuxv1.Scope_SCOPE_FILE_READ},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			set, ok := authscope.ScopesFromWire(wire)
			require.True(t, ok)
			assert.True(t, set.IsUnscoped(), "SCOPE_ALL is the absence of a limit")
			assert.True(t, set.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE),
				"an unscoped grant reaches a scope nobody named")
		})
	}

	// A NON-grantable value refuses the whole list rather than being dropped.
	// Dropping is the failure that looks safe: the remaining scopes would keep
	// working and nobody would notice the vocabulary drifted.
	for name, wire := range map[string][]leapmuxv1.Scope{
		"the zero value":   {leapmuxv1.Scope_SCOPE_FILE_READ, leapmuxv1.Scope_SCOPE_UNSPECIFIED},
		"a refusal marker": {leapmuxv1.Scope_SCOPE_FILE_READ, leapmuxv1.Scope_SCOPE_NEVER},
	} {
		t.Run(name+" refuses the whole list", func(t *testing.T) {
			t.Parallel()
			_, ok := authscope.ScopesFromWire(wire)
			assert.False(t, ok)
		})
	}
}
