package webauthn

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewGoWebAuthnValidatesTheRPID pins the reason the per-ceremony
// instance is built from the exported fields instead of copied.
//
// gowebauthn.Config carries an unexported `validated` flag that New() sets
// on success. A struct copy carries that flag with it, so New() on a copy
// short-circuits and never checks the RP ID the copy substituted -- the
// ceremony would then run with an RP ID the library would reject.
// Building a fresh literal leaves the flag false, so validation runs. This
// test fails the moment someone reintroduces the copy.
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
	} {
		badRP := rp
		badRP.RPID = bad
		_, err = newGoWebAuthn(badRP)
		assert.Errorf(t, err, "%q is not a valid RP ID and must not build a ceremony instance", bad)
	}
}

// TestCeremonyWebAuthnRebuildsRatherThanCopies pins the same property
// through the caller that substitutes a per-ceremony RP ID.
func TestCeremonyWebAuthnRebuildsRatherThanCopies(t *testing.T) {
	t.Parallel()

	rp := RPConfig{
		RPID:          "localhost",
		RPDisplayName: "LeapMux",
		RPOrigins:     []string{"http://localhost", "http://127.0.0.1"},
	}
	w, err := newGoWebAuthn(rp)
	require.NoError(t, err)
	s := &Service{w: w, rp: rp}

	// The default RP ID returns the shared instance untouched.
	same, err := s.ceremonyWebAuthn(rp.RPID)
	require.NoError(t, err)
	assert.Same(t, w, same)

	// A different allowed origin's RP ID builds a distinct instance that
	// carries the substituted value.
	other, err := s.ceremonyWebAuthn("127.0.0.1")
	require.NoError(t, err)
	require.NotSame(t, w, other)
	assert.Equal(t, "127.0.0.1", other.Config.RPID)
	assert.Equal(t, rp.RPOrigins, other.Config.RPOrigins,
		"the rebuilt instance must keep every relying-party parameter")
}
