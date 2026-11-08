package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

func TestWorkerPinListForwardsItsArguments(t *testing.T) {
	home := t.TempDir()
	selected := t.TempDir()
	for directory, id := range map[string]string{
		filepath.Join(home, ".config", "leapmux", "worker"): "default-worker",
		selected: "selected-worker",
	} {
		store, err := crossworker.NewPinStore(directory)
		require.NoError(t, err)
		require.NoError(t, store.Verify(id, []byte{1}, []byte{2}, []byte{3}))
	}
	for _, args := range [][]string{{"--data-dir", selected}, {"--data-dir=" + selected}} {
		result := invokeLifecycleCLI(t, home, append([]string{"worker", "cross-worker-pins", "list"}, args...)...)
		require.Zero(t, result.exitCode, result.stderr)
		var pins map[string]crossworker.PinEntry
		require.NoError(t, json.Unmarshal([]byte(result.stdout), &pins))
		require.Contains(t, pins, "selected-worker")
		assert.NotContains(t, pins, "default-worker")
		assert.Len(t, pins, 1)
	}
	result := invokeLifecycleCLI(t, home, "worker", "cross-worker-pins", "list", "--unknown-option")
	assert.Equal(t, 1, result.exitCode)
	assert.Contains(t, result.stderr, "flag provided but not defined")
}

func TestMissingModeVerbDiagnosticEndsWithANewline(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		var stdout, stderr bytes.Buffer
		code := runCLI([]string{mode, "--listen", ":4327"}, &stdout, &stderr, cliRunners{runMode: func(string, []string) error {
			t.Fatal("missing lifecycle verbs must not dispatch")
			return nil
		}})
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout.String())
		assert.Equal(t, fmt.Sprintf("error: the %s command is required before any flags, as in `leapmux %s start [flags]`\n", mode, mode), stderr.String())
	}
}

func TestDisableRejectsUnsupportedRuntimeTimeout(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		for _, timeout := range [][]string{{"--timeout=1s"}, {"--timeout", "1s"}} {
			_, _, err := loadLifecycleCommand(mode, "disable", append([]string{"--data-dir", t.TempDir()}, timeout...))
			require.ErrorContains(t, err, "flag provided but not defined")
		}
	}
}
