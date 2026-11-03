package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

// The re-registration advice appears on exactly the two errors no reconnect can
// fix, and every other error is returned unchanged.
func TestReregistrationAdvice(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state.json")

	keysRefused := reregistrationAdvice(
		errors.Join(errors.New("connect"), hub.ErrKeysRefused), statePath)
	require.Error(t, keysRefused)
	assert.Equal(t, 1, strings.Count(keysRefused.Error(), reregistrationSteps(statePath)),
		"the advice must appear exactly once")
	assert.Contains(t, keysRefused.Error(), statePath)

	unusable := reregistrationAdvice(
		errors.Join(errors.New("restore"), config.ErrUnusableRegisteredKeypair), statePath)
	require.Error(t, unusable)
	assert.Equal(t, 1, strings.Count(unusable.Error(), reregistrationSteps(statePath)),
		"the advice must appear exactly once")
	assert.Contains(t, unusable.Error(), statePath)

	other := errors.New("the hub is unreachable")
	assert.Equal(t, other, reregistrationAdvice(other, statePath),
		"an error a reconnect can fix gets no advice")
	assert.NoError(t, reregistrationAdvice(nil, statePath))
}

// An already-registered worker is told to deregister and register again. Wiping
// the state alone would leave the old row ACTIVE -- counting against
// max_workers_per_user and keeping its token valid for any copy of the file.
func TestAlreadyRegisteredError(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state.json")

	err := alreadyRegisteredError(statePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove --registration-key")
	assert.Equal(t, 1, strings.Count(err.Error(), reregistrationSteps(statePath)),
		"the steps must appear exactly once")
	assert.NotContains(t, err.Error(), "wipe",
		"wiping the state would leave the old row ACTIVE")
}

// The deregistration callback's clear must report a failure it cannot undo, so a
// worker does not exit with state.json still on disk and repeat the same silent
// failure at every start. An absent file is already the desired end state.
func TestClearDeletedWorkerState(t *testing.T) {
	t.Parallel()

	t.Run("an absent file is nil", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		assert.NoError(t, clearDeletedWorkerState(cfg))
	})

	t.Run("a present file is removed", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		require.NoError(t, os.WriteFile(cfg.StatePath(), []byte("{}"), 0o600))

		require.NoError(t, clearDeletedWorkerState(cfg))
		_, err := os.Stat(cfg.StatePath())
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("a path that cannot be removed states the path", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		// A NON-EMPTY directory cannot be removed by os.Remove on any platform,
		// which is the failure the discarded error used to hide.
		require.NoError(t, os.MkdirAll(filepath.Join(cfg.StatePath(), "inner"), 0o700))

		err := clearDeletedWorkerState(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), cfg.StatePath())
	})
}
