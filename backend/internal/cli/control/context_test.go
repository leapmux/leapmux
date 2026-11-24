package control_test

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestContextConfig_LoadSaveClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.True(t, cfg.IsEmpty())

	want := control.ContextConfig{
		Hub:       "https://hub.example.com",
		Worker:    "build-box",
		Workspace: "Release 1.4",
		Provider:  "claude",
	}
	require.NoError(t, control.SaveContext(want))

	got, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// context.json must never be treated as a credential file.
	credFiles, err := control.ListCredentialFiles()
	require.NoError(t, err)
	assert.Empty(t, credFiles)

	require.NoError(t, control.ClearContext())
	got, err = control.LoadContext()
	require.NoError(t, err)
	assert.True(t, got.IsEmpty())
}

func TestDefaultHub_UsesContextBeforeCredentialsAndDiscovery(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "control")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://ctx.example.com"}))
	hub, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://ctx.example.com", hub)

	// $LEAPMUX_HUB overrides context.json.
	t.Setenv("LEAPMUX_HUB", "https://env.example.com")
	hub, err = control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://env.example.com", hub)
}

func TestDiscoverLocalHub_FindsLiveSoloSocketAndIgnoresStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	// Use a short temp dir so Unix socket path stays under 104 bytes on macOS.
	root, err := os.MkdirTemp("", "lmxdisc-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloHubDir := filepath.Join(root, "solo", "hub")
	devHubDir := filepath.Join(root, "dev", "hub")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(soloHubDir, 0o700))
	require.NoError(t, os.MkdirAll(devHubDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	// Stale solo socket file (no listener) -> ignored.
	staleSock := filepath.Join(soloHubDir, "hub.sock")
	require.NoError(t, os.WriteFile(staleSock, nil, 0o600))
	assert.Equal(t, "", control.DiscoverLocalHub())

	// Live dev socket at dev/hub/hub.sock -> discovered when solo is stale.
	devSock := filepath.Join(devHubDir, "hub.sock")
	ln, err := net.Listen("unix", devSock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	assert.Equal(t, "unix:"+devSock, control.DiscoverLocalHub())

	// DefaultHub resolves to the discovered local IPC socket when no stored creds exist.
	hub, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "unix:"+devSock, hub)

	// When 2+ stored credentials exist with no default-hub, DefaultHub must NOT
	// silently fall through to a local discovered Hub.
	require.NoError(t, control.SaveCredentials("https://one.example.com", control.CredentialFile{
		HubURL: "https://one.example.com", AccessToken: "tok1", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, control.SaveCredentials("https://two.example.com", control.CredentialFile{
		HubURL: "https://two.example.com", AccessToken: "tok2", ExpiresAt: time.Now().Add(time.Hour),
	}))
	_, err = control.DefaultHub()
	require.Error(t, err)
	var noHub *control.NoHubError
	require.ErrorAs(t, err, &noHub)
	assert.Equal(t, 2, noHub.Stored)
}
