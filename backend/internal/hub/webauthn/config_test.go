package webauthn_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
)

func TestRPConfigFromSettings_UsesPublicURL(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()
	require.NoError(t, settings.KeyPublicURL.Set(ctx, set, "https://hub.example.com"))

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "127.0.0.1:8080")
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com", cfg.RPID)
	assert.Equal(t, []string{"https://hub.example.com"}, cfg.RPOrigins)
}

func TestRPConfigFromSettings_RejectsOriginlessListen(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()

	// Desktop local-only mode: no TCP listener, no public URL. Passkeys are
	// cleanly unavailable instead of silently broken against a bogus origin.
	_, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "")
	require.Error(t, err)
}

// TestRPConfigFromSettings_LoopbackCollapsesToLocalhost pins the one answer a
// loopback hub has for WebAuthn.
//
// A loopback bind is reachable at three spellings, but only one of them can
// run a ceremony. WebAuthn §5.1.3 requires the RP ID to be a valid domain
// string and excludes the address literal, so "127.0.0.1" is not an RP ID; and
// "localhost" is not a registrable-domain suffix of the effective domain
// "127.0.0.1", so it cannot stand in for one either. Both halves of that leave
// the IP-literal origins out of the allowlist entirely, which is what makes
// passkeysRunnableForOrigin report them honestly.
func TestRPConfigFromSettings_LoopbackCollapsesToLocalhost(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "127.0.0.1:8080")
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:8080"}, cfg.RPOrigins,
		"an IP-literal origin can run no ceremony, so it must not be allowlisted")
	// The default RP ID is a DOMAIN even though the bind address is not: this
	// is the value go-webauthn validates at construction, so an IP here makes
	// the whole service unbuildable rather than one ceremony fail.
	assert.Equal(t, "localhost", cfg.RPID)

	assert.True(t, cfg.AllowsOrigin("http://localhost:8080"))

	for _, origin := range []string{"http://127.0.0.1:8080", "http://[::1]:8080"} {
		assert.Falsef(t, cfg.AllowsOrigin(origin), "%s cannot run a ceremony and must not be allowed", origin)
	}
}

func TestRPConfigFromSettings_WildcardListenServesLoopback(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "0.0.0.0:9999")
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:9999"}, cfg.RPOrigins)
	assert.Equal(t, "localhost", cfg.RPID)
}

// TestRPConfigFromSettings_RejectsBareIPPublicURL pins the remote-IP case.
// It reports passkeys cleanly unavailable, the same way an absent listener
// does, rather than letting gowebauthn.New fail inside NewService and take
// every passkey call down with an opaque configuration error.
func TestRPConfigFromSettings_RejectsBareIPPublicURL(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()
	require.NoError(t, settings.KeyPublicURL.Set(ctx, set, "http://192.168.1.5:4327"))

	_, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "0.0.0.0:4327")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain name",
		"the error must say what to do, because the remedy is to set public_url to a hostname")
}

func TestRPConfigFromSettings_AllowsOriginOnlyAllowsConfiguredOrigins(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()
	require.NoError(t, settings.KeyPublicURL.Set(ctx, set, "http://localhost:4327"))

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "0.0.0.0:9999")
	require.NoError(t, err)
	assert.True(t, cfg.AllowsOrigin("http://localhost:4327"))
	// The IP-literal spelling of the SAME host is refused: no RP ID exists
	// that a browser on that page would accept.
	assert.False(t, cfg.AllowsOrigin("http://127.0.0.1:4327"))
	// An origin the hub does not serve is refused outright instead of
	// falling back to the default RP ID: the browser would accept the
	// ceremony (the RP ID stays a suffix of the page host) and the
	// finish-time origin check would reject it -- interactive biometric
	// work for a guaranteed-dead ceremony. A client-claimed origin can
	// never widen the credential scope either, because it matches nothing.
	assert.False(t, cfg.AllowsOrigin("https://evil.example.com"))
	// A port-mismatched spelling of the allowed host is refused for the
	// same reason: the host matches, the origin does not.
	assert.False(t, cfg.AllowsOrigin("http://localhost:9999"))
	// An empty origin (a non-browser client without an Origin header) is
	// allowed: there is no browser ceremony to mislead.
	assert.True(t, cfg.AllowsOrigin(""))
}
