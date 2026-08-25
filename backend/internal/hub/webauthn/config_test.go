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

func TestRPConfigFromSettings_LoopbackAliasesMatchOriginHost(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "127.0.0.1:8080")
	require.NoError(t, err)
	assert.Contains(t, cfg.RPOrigins, "http://127.0.0.1:8080")
	assert.Contains(t, cfg.RPOrigins, "http://localhost:8080")
	assert.Contains(t, cfg.RPOrigins, "http://[::1]:8080")
	// The default RP ID is the base origin's host; each allowlisted origin
	// maps to its own host because browsers reject an RP ID that is not a
	// suffix of the page origin (localhost is not a suffix of 127.0.0.1).
	assert.Equal(t, "127.0.0.1", cfg.RPID)
	rpID, allowed := cfg.RPIDForOrigin("http://localhost:8080")
	require.True(t, allowed)
	assert.Equal(t, "localhost", rpID)
	rpID, allowed = cfg.RPIDForOrigin("http://127.0.0.1:8080")
	require.True(t, allowed)
	assert.Equal(t, "127.0.0.1", rpID)
}

func TestRPConfigFromSettings_WildcardListenServesLoopback(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "0.0.0.0:9999")
	require.NoError(t, err)
	assert.Contains(t, cfg.RPOrigins, "http://localhost:9999")
	assert.Contains(t, cfg.RPOrigins, "http://127.0.0.1:9999")
}

func TestRPConfigFromSettings_RPIDForOriginOnlyAllowsConfiguredOrigins(t *testing.T) {
	st := testutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	ctx := context.Background()
	require.NoError(t, settings.KeyPublicURL.Set(ctx, set, "http://localhost:4327"))

	cfg, err := webauthn.RPConfigFromSettings(set.Snapshot(ctx), "0.0.0.0:9999")
	require.NoError(t, err)
	rpID, allowed := cfg.RPIDForOrigin("http://127.0.0.1:4327")
	require.True(t, allowed)
	assert.Equal(t, "127.0.0.1", rpID)
	// An origin the hub does not serve is refused outright instead of
	// falling back to the default RP ID: the browser would accept the
	// ceremony (the RP ID stays a suffix of the page host) and the
	// finish-time origin check would reject it -- interactive biometric
	// work for a guaranteed-dead ceremony. A client-claimed origin can
	// never widen the credential scope either, because it matches nothing.
	_, allowed = cfg.RPIDForOrigin("https://evil.example.com")
	assert.False(t, allowed)
	// A port-mismatched spelling of the allowed host is refused for the
	// same reason: the host matches, the origin does not.
	_, allowed = cfg.RPIDForOrigin("http://localhost:9999")
	assert.False(t, allowed)
	// An empty origin (a non-browser client without an Origin header) keeps
	// the default RPID: there is no browser ceremony to mislead.
	rpID, allowed = cfg.RPIDForOrigin("")
	require.True(t, allowed)
	assert.Equal(t, cfg.RPID, rpID)
}
