package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesktopSoloConfig_PassesDevFrontendWhenSet(t *testing.T) {
	t.Parallel()

	cfg := desktopSoloConfig("http://localhost:4328")
	assert.True(t, cfg.NoTCP)
	assert.True(t, cfg.SkipBanner)
	assert.Equal(t, []string{"-dev-frontend", "http://localhost:4328"}, cfg.Args)
}

func TestDesktopSoloConfig_OmitsDevFrontendWhenEmpty(t *testing.T) {
	t.Parallel()

	cfg := desktopSoloConfig("")
	assert.True(t, cfg.NoTCP)
	assert.Empty(t, cfg.Args)
}

func TestDesktopSoloConfigFromEnv_ReadsContractEnv(t *testing.T) {
	// Not parallel: t.Setenv mutates process env for this test.
	t.Setenv(contracts.EnvDevFrontend, "http://localhost:4328")
	cfg := desktopSoloConfigFromEnv()
	assert.Equal(t, []string{"-dev-frontend", "http://localhost:4328"}, cfg.Args)

	t.Setenv(contracts.EnvDevFrontend, "")
	cfg = desktopSoloConfigFromEnv()
	assert.Empty(t, cfg.Args)
}

// Pins the contract name the Rust debug spawn writes: a rename here without a
// matching sidecar.rs change would leave extra listen addresses on the
// embedded SPA again.
func TestDesktopSoloConfig_UsesContractEnvName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "LEAPMUX_HUB_DEV_FRONTEND", contracts.EnvDevFrontend)
}

// The Rust debug spawn is the only writer of the env. If it stops setting
// ENV_DEV_FRONTEND to the Vite URL, Go never sees Args and extras fall back
// to the embedded SPA — the original bug.
func TestRustDebugSpawn_SetsDevFrontendEnv(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	sidecarPath := filepath.Join(filepath.Dir(thisFile), "..", "rust", "src", "sidecar.rs")
	src, err := os.ReadFile(sidecarPath)
	require.NoError(t, err)

	body := string(src)
	assert.Contains(t, body, "ENV_DEV_FRONTEND")
	assert.Contains(t, body, `http://localhost:4328`)
	assert.Contains(t, body, ".env(ENV_DEV_FRONTEND, DEV_FRONTEND_URL)")
}
