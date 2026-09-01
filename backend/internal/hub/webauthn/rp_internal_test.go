package webauthn

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewGoWebAuthnValidatesTheRPID pins the reason the instance is built
// from the exported fields instead of copied.
//
// gowebauthn.Config carries an unexported `validated` flag that New() sets
// on success. A struct copy carries that flag with it, so New() on a copy
// short-circuits and never checks the RP ID the copy holds -- a ceremony
// would then run with an RP ID the library would reject. Building a fresh
// literal leaves the flag false, so validation runs. This test fails the
// moment someone reintroduces the copy.
//
// This is the hub's ONE construction path, so it is also where a bad RP ID
// from settings is caught: NewService fails rather than a ceremony.
func TestNewGoWebAuthnValidatesTheRPID(t *testing.T) {
	t.Parallel()

	rp := RPConfig{
		RPID:          "localhost",
		RPDisplayName: "LeapMux",
		RPOrigins:     []string{"http://localhost"},
	}

	w, err := newGoWebAuthn(rp)
	require.NoError(t, err)
	require.NotNil(t, w)

	// An RP ID must be a bare registrable domain. These are the spellings
	// protocol.ValidateRPID refuses, and a copied config would evade
	// every one of them.
	for _, bad := range []string{
		"http://localhost",
		"localhost:8080",
		"not a domain",
		"https://example.com/path",
		// Address literals. §5.1.3 names these as the case a valid domain
		// string excludes, so a ceremony under one could never complete --
		// the browser refused it even while go-webauthn still accepted it.
		// rpIDForHost maps every loopback spelling to "localhost" so this
		// value cannot reach the library from a hub configuration.
		"127.0.0.1",
		"::1",
		"192.168.1.5",
	} {
		badRP := rp
		badRP.RPID = bad
		_, err = newGoWebAuthn(badRP)
		assert.Errorf(t, err, "%q is not a valid RP ID and must not build a ceremony instance", bad)
	}
}

// TestEveryAllowedOriginResolvesToTheOneRPID pins the invariant the service
// is built on: the hub has ONE relying-party ID, and every origin it allows
// resolves to it.
//
// The ceremony layer used to carry an RP ID from Begin to Finish -- through
// the session payload, the user loader and a per-ceremony go-webauthn
// instance -- because the loopback IP spellings each needed their own. Those
// spellings can run no ceremony and are no longer allowlisted, so the whole
// mechanism went. Nothing else re-derives an RP ID per origin, so a second
// allowed host with a different name would now run a ceremony under an RP ID
// the browser never agreed to, silently. This test is what fails first.
func TestEveryAllowedOriginResolvesToTheOneRPID(t *testing.T) {
	t.Parallel()

	for _, base := range []string{
		"https://hub.example.com",
		"http://localhost:4327",
		"http://127.0.0.1:4327",
		"http://[::1]:4327",
		"https://hub.example.com:8443",
	} {
		u, err := url.Parse(base)
		require.NoError(t, err)
		rp := RPConfig{RPID: rpIDForHost(u.Hostname()), RPOrigins: allowedOrigins(u)}

		for _, origin := range rp.RPOrigins {
			ou, err := url.Parse(origin)
			require.NoError(t, err)
			assert.Equalf(t, rp.RPID, rpIDForHost(ou.Hostname()),
				"base %s allows origin %s, which resolves to a DIFFERENT RP ID", base, origin)
		}
		// An allowlist that went empty would satisfy the loop vacuously.
		assert.NotEmptyf(t, rp.RPOrigins, "base %s must allow at least one origin", base)
	}
}
