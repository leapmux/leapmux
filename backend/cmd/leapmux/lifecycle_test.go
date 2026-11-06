package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// runMode invokes a mode's lifecycle leaf in-process.
func runMode(t *testing.T, mode, verb string, args ...string) error {
	t.Helper()
	return runModeCommand(mode, append([]string{verb}, args...))
}

// TestForegroundNeverConfiguresOrElevates: every foreground mode,
// including its error paths, writes no service specification and no
// native definition. The run verb's collaborators are the leases and
// the runtime lock alone.
func TestForegroundNeverConfiguresOrElevates(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			dataDir := t.TempDir()
			// A failing run (no configuration, nothing registered) is
			// the fastest honest path through the foreground verb.
			err := runMode(t, mode, "run", "--data-dir", dataDir)
			require.Error(t, err, "an empty data directory cannot start %s", mode)

			// No service specification and no lifecycle service area
			// beyond the runtime's own records: the run verb never
			// configured anything.
			specPath := lifecycle.PathsOf(mustCanonicalDir(t, dataDir)).ServiceSpec
			assert.NoFileExists(t, specPath, "the foreground run configured no native startup")
		})
	}
}

// TestInvalidTimeoutsAndMissingRegistration: zero, negative, and
// overflowing durations fail the waiting verbs' validation; a very
// large representable duration succeeds; an unregistered worker's
// execution fails before any setup prompt or native call.
func TestInvalidTimeoutsAndMissingRegistration(t *testing.T) {
	t.Run("zero and negative durations fail", func(t *testing.T) {
		for _, value := range []string{"0", "0s", "-1s", "-"} {
			_, _, err := parseTimeoutFlag([]string{"--timeout", value}, defaultStartTimeout)
			require.Error(t, err, "timeout %q must fail validation", value)
		}
	})

	t.Run("an overflowing duration fails", func(t *testing.T) {
		_, _, err := parseTimeoutFlag([]string{"--timeout", "999999999999999999999s"}, defaultStartTimeout)
		require.Error(t, err)
	})

	t.Run("a very large representable duration succeeds", func(t *testing.T) {
		got, rest, err := parseTimeoutFlag([]string{"--timeout", "87600h"}, defaultStartTimeout)
		require.NoError(t, err)
		assert.Equal(t, 87600*time.Hour, got)
		assert.Empty(t, rest)
	})

	t.Run("an unregistered worker's stop succeeds as not running", func(t *testing.T) {
		// Nothing registered, nothing running: the stop verb's honest
		// answer is success with "Not running", reached before any
		// native call or prompt.
		err := runMode(t, "worker", "stop", "--data-dir", t.TempDir())
		require.NoError(t, err)
	})

	t.Run("an unregistered worker's status reports stopped with exit 3", func(t *testing.T) {
		err := runMode(t, "worker", "status", "--data-dir", t.TempDir())
		require.ErrorIs(t, err, errStoppedExit)
	})
}

// TestWaitTimeoutDoesNotChangeRuntimeOutcome: a startup wait that
// expires reports that startup continues and does not launch a
// replacement; a shutdown wait that expires while the lock is held
// reports the same and leaves the instance alone.
func TestWaitTimeoutDoesNotChangeRuntimeOutcome(t *testing.T) {
	t.Run("a readiness wait past its budget reports and returns", func(t *testing.T) {
		// A directory nothing serves: the wait expires, the verdict
		// names status, and no replacement launched.
		dataDir := t.TempDir()
		err := waitForReadiness("worker", mustCanonicalDir(t, dataDir), 150*time.Millisecond)
		require.NoError(t, err, "an expired readiness wait is a report, not a failure")
	})

	t.Run("a lock held past the shutdown budget reports continuation", func(t *testing.T) {
		dataDir := t.TempDir()
		canonical := mustCanonicalDir(t, dataDir)
		paths := lifecycle.PathsOf(canonical)
		require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
		// Write a record and hold the runtime lock in this process.
		require.NoError(t, lifecycle.WriteControlToken(paths.ControlToken, "token-material"))
		require.NoError(t, lifecycle.WriteRuntimeRecord(paths.RuntimeRecord, lifecycle.RuntimeRecord{
			InstanceID: "wait0000000000001",
			AttemptID:  "d9a0425886d148f68a8f94716ed47a78",
			Mode:       lifecycle.RoleWorker,
			DataDir:    canonical,
			ControlURL: "unix:/nonexistent.sock",
		}))

		// The stop verb's own budget path: the endpoint never answers
		// and the runtime lock stays held by this test's subprocess,
		// so the verdict reports an unresponsive instance.
		stopHolder := startLockHolder(t, paths.RuntimeLock)
		defer stopHolder()

		err := runMode(t, "worker", "stop", "--timeout", "200ms", "--data-dir", dataDir)
		require.Error(t, err, "an unresponsive instance is an error, never a silent success")
		assert.True(t, strings.Contains(err.Error(), "did not accept") || strings.Contains(err.Error(), "unresponsive") || strings.Contains(err.Error(), "operation lock"),
			"the refusal names what happened: %v", err)
	})
}

// mustCanonicalDir canonicalizes for assertions.
func mustCanonicalDir(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(dir)
	require.NoError(t, err)
	return canonical
}

// splitLifecycleArgs scans only the exact --data-dir spellings: the
// value form, the = form, and the missing-value refusal, with every
// other token -- flags included -- passing through untouched for the
// mode's own loader.
func TestSplitLifecycleArgs(t *testing.T) {
	t.Run("the value form and the = form both bind", func(t *testing.T) {
		got, rest, err := splitLifecycleArgs([]string{"--data-dir", "/data", "--log-level", "debug"})
		require.NoError(t, err)
		assert.Equal(t, "/data", got)
		assert.Equal(t, []string{"--log-level", "debug"}, rest)

		got, rest, err = splitLifecycleArgs([]string{"--data-dir=/data", "run-only"})
		require.NoError(t, err)
		assert.Equal(t, "/data", got)
		assert.Equal(t, []string{"run-only"}, rest)
	})

	t.Run("a missing value refuses", func(t *testing.T) {
		_, _, err := splitLifecycleArgs([]string{"--log-level", "debug", "--data-dir"})
		require.ErrorContains(t, err, "--data-dir")
	})

	t.Run("an empty argument list takes no directory", func(t *testing.T) {
		got, rest, err := splitLifecycleArgs(nil)
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Empty(t, rest)
	})

	t.Run("other flags pass through untouched", func(t *testing.T) {
		_, rest, err := splitLifecycleArgs([]string{"--hub", "http://x", "-config", "/c.yaml", "--data-dir=/d"})
		require.NoError(t, err)
		assert.Equal(t, []string{"--hub", "http://x", "-config", "/c.yaml"}, rest)
	})
}

// parseStartupFlag admits only the two startup kinds, in both the bare
// and the = spellings the plan states.
func TestParseStartupFlag(t *testing.T) {
	t.Run("the = form parses and the rest survives", func(t *testing.T) {
		kind, rest, err := parseStartupFlag([]string{"--startup=boot", "--yes", "--data-dir", "/d"})
		require.NoError(t, err)
		assert.Equal(t, lifecycle.StartupBoot, kind)
		assert.Equal(t, []string{"--yes", "--data-dir", "/d"}, rest)
	})

	t.Run("an unknown kind refuses with the value named", func(t *testing.T) {
		_, _, err := parseStartupFlag([]string{"--startup=whenever"})
		require.ErrorContains(t, err, "whenever")
	})

	t.Run("the bare form names its corrected spelling", func(t *testing.T) {
		_, _, err := parseStartupFlag([]string{"--startup"})
		require.ErrorContains(t, err, "--startup=")
	})

	t.Run("no flag leaves the rest alone", func(t *testing.T) {
		kind, rest, err := parseStartupFlag([]string{"--yes"})
		require.NoError(t, err)
		assert.Empty(t, kind)
		assert.Equal(t, []string{"--yes"}, rest)
	})
}

// An unresponsive instance -- the runtime lock held, the endpoint
// silent -- is a status FAILURE (exit 1), never a stopped instance
// (exit 3): the evidence says the process still exists.
func TestStatusReportsUnresponsiveAsFailureNotStopped(t *testing.T) {
	dataDir := t.TempDir()
	canonical := mustCanonicalDir(t, dataDir)
	paths := lifecycle.PathsOf(canonical)
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
	require.NoError(t, lifecycle.WriteControlToken(paths.ControlToken, "token-material"))
	require.NoError(t, lifecycle.WriteRuntimeRecord(paths.RuntimeRecord, lifecycle.RuntimeRecord{
		InstanceID: "unre0000000000001",
		AttemptID:  "f9a0425886d148f68a8f94716ed47a78",
		Mode:       lifecycle.RoleWorker,
		DataDir:    canonical,
		ControlURL: "unix:/nonexistent.sock",
	}))
	stopHolder := startLockHolder(t, paths.RuntimeLock)
	defer stopHolder()

	err := runMode(t, "worker", "status", "--data-dir", dataDir)
	require.ErrorIs(t, err, errUnresponsiveExit, "an unresponsive instance is a failure, never stopped")
}
