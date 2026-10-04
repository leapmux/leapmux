package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// sidecarSource returns the source of desktop/rust/src/sidecar.rs.
func sidecarSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "rust", "src", "sidecar.rs"))
	require.NoError(t, err)
	return string(src)
}

// rustFunction returns the top-level Rust function of src that signature
// begins, from the signature to its closing brace at the start of a line.
func rustFunction(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	require.GreaterOrEqual(t, start, 0, "sidecar.rs defines %s", signature)
	end := strings.Index(src[start:], "\n}\n")
	require.GreaterOrEqual(t, end, 0, "%s ends with a closing brace at the start of a line", signature)
	return src[start : start+end+len("\n}")]
}

// The Rust debug spawn is the only writer of the env. If it stops setting
// ENV_DEV_FRONTEND to the contract DEV URL, Go never sees Args and extras
// fall back to the embedded SPA — the original bug.
func TestRustDebugSpawn_SetsDevFrontendEnv(t *testing.T) {
	t.Parallel()

	require.Equal(t, "http://localhost:4328", contracts.DevFrontendURL)

	src := sidecarSource(t)
	assert.Contains(t, rustFunction(t, src, "fn bootstrap_dev_sidecar("), "dev_sidecar_command(",
		"the debug spawn builds its command with dev_sidecar_command")
	assert.Contains(t, rustFunction(t, src, "fn dev_sidecar_command("), ".env(ENV_DEV_FRONTEND, crate::DEV_FRONTEND_URL)")
}

// Release spawn must strip a leaked LEAPMUX_HUB_DEV_FRONTEND so packaged
// apps never enable DevProxy from the parent environment.
func TestRustReleaseSpawn_ClearsDevFrontendEnv(t *testing.T) {
	t.Parallel()

	src := sidecarSource(t)
	assert.Contains(t, rustFunction(t, src, "fn spawn_stdio_sidecar("), "stdio_sidecar_command(",
		"the release spawn builds its command with stdio_sidecar_command")
	assert.Contains(t, rustFunction(t, src, "fn stdio_sidecar_command("), "command.env_remove(ENV_DEV_FRONTEND)")
}

// The sidecar starts with no argument, which is how an agent's CLI starts the
// worker's executable as a provider helper (worker.RunAgentHelper). Both spawn
// paths must strip an inherited LEAPMUX_AGENT_HELPER, or a desktop app that a
// shell of an Amp agent launched would run its sidecar as that helper. Each
// spawn builds its command in a function of its own, and each of those
// functions removes the variable.
func TestRustSpawns_ClearAgentHelperEnv(t *testing.T) {
	t.Parallel()

	require.Equal(t, "LEAPMUX_AGENT_HELPER", contracts.EnvAgentHelper)

	src := sidecarSource(t)
	for spawn, builder := range map[string]string{
		"fn bootstrap_dev_sidecar(": "dev_sidecar_command(",
		"fn spawn_stdio_sidecar(":   "stdio_sidecar_command(",
	} {
		assert.Contains(t, rustFunction(t, src, spawn), builder, "%s builds its command with %s", spawn, builder)
		assert.Contains(t, rustFunction(t, src, "fn "+builder), "command.env_remove(ENV_AGENT_HELPER)",
			"%s strips the helper variable", builder)
	}
}
