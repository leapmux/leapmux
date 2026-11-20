package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		commit, err := store.Resolve(context.Background(), id, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(context.Background()))
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

func TestWorkerPinRemoveUpdatesAnExistingHandle(t *testing.T) {
	home, workerDir := t.TempDir(), t.TempDir()
	store, err := crossworker.NewPinStore(workerDir)
	require.NoError(t, err)
	for _, workerID := range []string{"worker-to-remove", "worker-to-keep"} {
		commit, err := store.Resolve(t.Context(), workerID, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))
	}
	before, err := store.List(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 2)
	waitingCommit, err := store.Resolve(t.Context(), "worker-to-remove", []byte{1}, []byte{2}, []byte{3})
	require.NoError(t, err)

	result := invokeLifecycleCLI(t, home, "worker", "cross-worker-pins", "remove",
		"--data-dir="+workerDir, "--target-worker-id=worker-to-remove")
	require.Zero(t, result.exitCode, result.stderr)
	assert.JSONEq(t, `{"removed_target_worker_id":"worker-to-remove"}`, result.stdout)
	require.ErrorContains(t, waitingCommit(t.Context()), "removal revision changed")
	after, err := store.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, map[string]crossworker.PinEntry{"worker-to-keep": before["worker-to-keep"]}, after)
}

func TestWorkerPinListAndShowDistinguishReadErrorsFromMissingPins(t *testing.T) {
	home, workerDir := t.TempDir(), t.TempDir()
	path := filepath.Join(workerDir, "cross_worker_pins.json")
	require.NoError(t, os.WriteFile(path, []byte("{invalid"), 0o600))
	list := []string{"worker", "cross-worker-pins", "list", "--data-dir=" + workerDir}
	show := []string{"worker", "cross-worker-pins", "show", "--data-dir=" + workerDir, "--target-worker-id=missing"}
	for _, args := range [][]string{list, show} {
		result := invokeLifecycleCLI(t, home, args...)
		assert.Equal(t, 1, result.exitCode)
		assert.Empty(t, result.stdout)
		assert.Contains(t, result.stderr, "parse pins")
	}

	require.NoError(t, os.WriteFile(path, []byte(`{"pins":{}}`), 0o600))
	result := invokeLifecycleCLI(t, home, list...)
	require.Zero(t, result.exitCode, result.stderr)
	assert.JSONEq(t, "{}", result.stdout)
	result = invokeLifecycleCLI(t, home, show...)
	assert.Equal(t, 1, result.exitCode)
	assert.Empty(t, result.stdout)
	assert.Contains(t, result.stderr, "no pin recorded")
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
