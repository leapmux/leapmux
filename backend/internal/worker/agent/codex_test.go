//go:build unix

package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
)

func TestCodex_LoginShellEnvUsesCodexMarkers(t *testing.T) {
	ctx := context.Background()

	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Dir = t.TempDir()
	cmd.Env = filterEnv(cmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")

	foundWorker := false
	foundCodexCI := false
	foundThreadID := false
	for _, e := range cmd.Env {
		if e == "LEAPMUX_WORKER=1" {
			foundWorker = true
		}
		if e == "CODEX_CI=1" {
			foundCodexCI = true
		}
		if strings.HasPrefix(e, "CODEX_THREAD_ID=") {
			foundThreadID = true
		}
	}
	assert.True(t, foundWorker, "LEAPMUX_WORKER=1 should be in env")
	assert.False(t, foundCodexCI, "CODEX_CI=1 should NOT be in env without login shell")
	assert.False(t, foundThreadID, "CODEX_THREAD_ID should be filtered from env")

	shellCmd, _, _ := buildShellWrappedCommand(ctx, testutil.TestShell(), true, "codex", []string{"CODEX_CI"}, []string{"app-server"}, nil, t.TempDir())
	shellCmd.Env = filterEnv(shellCmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	shellCmd.Env = append(shellCmd.Env, "LEAPMUX_WORKER=1", "CODEX_CI=1")

	foundWorker = false
	foundCodexCI = false
	foundThreadID = false
	for _, e := range shellCmd.Env {
		if e == "LEAPMUX_WORKER=1" {
			foundWorker = true
		}
		if e == "CODEX_CI=1" {
			foundCodexCI = true
		}
		if strings.HasPrefix(e, "CODEX_THREAD_ID=") {
			foundThreadID = true
		}
	}
	assert.True(t, foundWorker, "LEAPMUX_WORKER=1 should be in shell-wrapped env")
	assert.True(t, foundCodexCI, "CODEX_CI=1 should be in shell-wrapped env")
	assert.False(t, foundThreadID, "CODEX_THREAD_ID should remain filtered in shell-wrapped env")
}
