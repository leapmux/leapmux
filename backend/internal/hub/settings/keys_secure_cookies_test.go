package settings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// The rule: the configured key, OR the protocol a trusted proxy verified. The
// verified protocol only ever turns the policy ON.
//
// It exists because the hub terminates no TLS. An operator who put a hub
// behind a TLS proxy had to know to set `secure_cookies` by hand, and until
// they did, a hub answering every request over verified HTTPS still wrote
// session cookies with no Secure attribute.
func TestSecureCookiesFor(t *testing.T) {
	t.Parallel()
	manager := settings.NewManager(hubtestutil.OpenTestStore(t), nil, settings.CoreDescriptors())
	require.NoError(t, manager.Load(context.Background()))
	snap := manager.Snapshot(context.Background())

	plain := context.Background()
	verified := peer.WithHTTPS(context.Background(), true)

	assert.False(t, settings.SecureCookiesFor(plain, snap),
		"the shipped default writes a plain cookie over plain HTTP")
	assert.True(t, settings.SecureCookiesFor(verified, snap),
		"a trusted proxy that verified TLS turns the policy on with no setting")

	require.NoError(t, manager.Update(context.Background(), settings.KeySecureCookies, []byte(`true`)))
	on := manager.Snapshot(context.Background())
	assert.True(t, settings.SecureCookiesFor(plain, on),
		"the setting still decides on its own")
	assert.True(t, settings.SecureCookiesFor(verified, on))
}

// public_url wins whenever it is set, so an operator who stated the address a
// browser really uses keeps that answer whatever a proxy reports.
func TestBaseURLFor(t *testing.T) {
	t.Parallel()
	manager := settings.NewManager(hubtestutil.OpenTestStore(t), nil, settings.CoreDescriptors())
	require.NoError(t, manager.Load(context.Background()))
	snap := manager.Snapshot(context.Background())

	assert.Equal(t, "http://127.0.0.1:4327",
		settings.BaseURLFor(context.Background(), snap, "127.0.0.1:4327"))
	assert.Equal(t, "https://127.0.0.1:4327",
		settings.BaseURLFor(peer.WithHTTPS(context.Background(), true), snap, "127.0.0.1:4327"),
		"a verified TLS request builds https links without a second setting")

	require.NoError(t, manager.Update(context.Background(), settings.KeyPublicURL, []byte(`"https://hub.example.test"`)))
	published := manager.Snapshot(context.Background())
	assert.Equal(t, "https://hub.example.test",
		settings.BaseURLFor(context.Background(), published, "127.0.0.1:4327"),
		"public_url outranks the per-request scheme")
}
