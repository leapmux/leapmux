//go:build unix

package acp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

func TestStartClosesThePreparedLaunchWhenInitializeMetadataCannotBeEncoded(t *testing.T) {
	shell, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("Nushell is needed to check real invocation files")
	}
	artifacts := t.TempDir()
	dir := t.TempDir()
	config := filepath.Join(dir, "nushell")
	require.NoError(t, os.Mkdir(config, 0o700))
	t.Setenv("TMPDIR", artifacts)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	const candidate = "leapmux-acp-start-peer"
	invoked := filepath.Join(dir, "invoked")
	profile := "def --wrapped " + candidate + " [...args: string] { 'started' | save " + strconv.Quote(invoked) + " }\n"
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.nu"), []byte(profile), 0o600))

	base := &Base{}
	spec := StartSpec[Base]{
		Registration: agent.Registration{Locator: launch.Binaries(candidate)},
		NewAgent:     func() *Base { return base },
		Base:         func(base *Base) *Base { return base },
		Configure: func(*Base, agent.ProviderServices) Hooks {
			return Hooks{InitializeMeta: map[string]any{"invalid": make(chan bool)}}
		},
	}
	started, err := Start(t.Context(), agent.Options{Shell: shell, LoginShell: true, WorkingDir: dir}, agent.NewProviderServices(&agenttest.Sink{}), spec)
	require.ErrorContains(t, err, "marshal initialize params")
	require.Nil(t, started)
	if cmd := base.Cmd(); cmd != nil {
		assert.Nil(t, cmd.Process, "invalid metadata must fail before the shell starts")
	}
	assert.NoFileExists(t, invoked)
	entries, err := os.ReadDir(artifacts)
	require.NoError(t, err)
	assert.Empty(t, entries, "the rejected launch must release its Nushell invocation directory")
}
