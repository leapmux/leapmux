package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/tofupins"
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
		commit, err := store.Resolve(t.Context(), id, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))
	}
	for _, args := range [][]string{{"--data-dir", selected}, {"--data-dir=" + selected}} {
		result := invokeLifecycleCLI(t, home, append([]string{"worker", "cross-worker-pins", "list", "--json"}, args...)...)
		require.Zero(t, result.exitCode, result.stderr)
		var envelope struct {
			Data struct {
				Pins []crossWorkerPin `json:"pins"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.stdout), &envelope), result.stdout)
		require.Len(t, envelope.Data.Pins, 1)
		assert.Equal(t, "selected-worker", envelope.Data.Pins[0].WorkerID)
	}
	result := invokeLifecycleCLI(t, home, "worker", "cross-worker-pins", "list", "--unknown-option")
	assert.Equal(t, cli.ExitUsage, result.exitCode)
	assert.Contains(t, result.stderr, "unknown flag: --unknown-option")
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
	before, err := store.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 2)
	waitingCommit, err := store.Resolve(t.Context(), "worker-to-remove", []byte{1}, []byte{2}, []byte{3})
	require.NoError(t, err)

	result := invokeLifecycleCLI(t, home, "worker", "cross-worker-pins", "remove",
		"worker-to-remove", "--data-dir="+workerDir, "--json")
	require.Zero(t, result.exitCode, result.stderr)
	assert.JSONEq(t, `{"data":{"removed":"worker-to-remove"}}`, result.stdout)
	require.ErrorContains(t, waitingCommit(t.Context()), "removal revision changed")
	after, err := store.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Equal(t, map[string]tofupins.Entry{"worker-to-keep": before["worker-to-keep"]}, after)
}

func TestWorkerPinListAndGetDistinguishReadErrorsFromMissingPins(t *testing.T) {
	home, workerDir := t.TempDir(), t.TempDir()
	path := filepath.Join(workerDir, "cross_worker_pins.json")
	require.NoError(t, os.WriteFile(path, []byte("{invalid"), 0o600))
	list := []string{"worker", "cross-worker-pins", "list", "--data-dir=" + workerDir}
	get := []string{"worker", "cross-worker-pins", "get", "missing", "--data-dir=" + workerDir}
	for _, args := range [][]string{list, get} {
		result := invokeLifecycleCLI(t, home, args...)
		assert.Equal(t, cli.ExitFailure, result.exitCode)
		assert.Empty(t, result.stdout)
		assert.Contains(t, result.stderr, "parse pins")
	}

	require.NoError(t, os.WriteFile(path, []byte(`{"pins":{}}`), 0o600))
	result := invokeLifecycleCLI(t, home, append(list, "--json")...)
	require.Zero(t, result.exitCode, result.stderr)
	assert.JSONEq(t, `{"data":{"pins":[]}}`, result.stdout)
	result = invokeLifecycleCLI(t, home, get...)
	assert.Equal(t, cli.ExitNotFound, result.exitCode)
	assert.Empty(t, result.stdout)
	assert.Contains(t, result.stderr, "no pin for the sibling Worker")
}

// A flag before the command of a mode fails before anything runs, and the
// refusal states where the flag goes.
func TestAFlagBeforeTheModeCommandStatesWhereItGoes(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		o := runTestCLI(t, mode, "--listen", ":4327")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Empty(t, o.stdout)
		assert.Equal(t, fmt.Sprintf("error: usage: unknown flag: --listen\n"+
			"hint: a flag goes after the command that takes it, as in \"leapmux %s <command> [flags]\"\n"+
			"hint: a value that starts with \"-\" goes after \"--\", as in: -- -v\n"+
			"hint: run \"leapmux %s --help\" for usage\n", mode, mode), o.stderr)
	}
}

func TestDisableRejectsUnsupportedRuntimeTimeout(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		for _, timeout := range [][]string{{"--timeout=1s"}, {"--timeout", "1s"}} {
			_, _, err := loadLifecycleCommand(mode, "disable", append([]string{"--data-dir", t.TempDir()}, timeout...))
			require.ErrorContains(t, err, "unknown flag: --timeout")
		}
	}
}
