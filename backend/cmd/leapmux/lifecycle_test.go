package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestStartupFailureKeepsReasonWhenLogCannotBeRead(t *testing.T) {
	reason := "worker database initialization failed"
	status := lifecycle.Status{
		Process: lifecycle.ProcessStatus{State: lifecycle.ProcessFailed},
		Attempt: lifecycle.AttemptStatus{Error: &reason, LogPath: new(t.TempDir())},
	}
	err := reportStartupFailure("worker", lifecycle.Paths{}, status)
	require.ErrorContains(t, err, reason)
}

func TestForegroundResultPreservesIndependentFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Time{})
	defer cancelDeadline()
	storageFailure := errors.New("database close failed")
	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
		want error
	}{
		{name: "clean stop", ctx: canceled},
		{name: "requested cancellation", ctx: canceled, err: context.Canceled},
		{name: "wrapped requested cancellation", ctx: canceled, err: fmt.Errorf("migration: %w", context.Canceled)},
		{name: "joined cancellations", ctx: canceled, err: errors.Join(context.Canceled, fmt.Errorf("startup: %w", context.Canceled))},
		{name: "automatic transaction rollback", ctx: canceled, err: errors.Join(context.Canceled, sql.ErrTxDone)},
		{name: "transaction error without cancellation", ctx: canceled, err: sql.ErrTxDone, want: sql.ErrTxDone},
		{name: "rollback and independent failure", ctx: canceled, err: errors.Join(context.Canceled, sql.ErrTxDone, storageFailure), want: storageFailure},
		{name: "independent failure", ctx: canceled, err: storageFailure, want: storageFailure},
		{name: "joined cleanup failure", ctx: canceled, err: errors.Join(context.Canceled, storageFailure), want: storageFailure},
		{name: "wrapped joined cleanup failure", ctx: canceled, err: fmt.Errorf("shutdown: %w", errors.Join(context.Canceled, storageFailure)), want: storageFailure},
		{name: "unrequested cancellation", ctx: context.Background(), err: context.Canceled, want: context.Canceled},
		{name: "deadline is not a stop", ctx: deadline, err: context.DeadlineExceeded, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := foregroundResult(test.ctx, test.err)
			if test.want == nil {
				require.NoError(t, got)
			} else {
				require.ErrorIs(t, got, test.want)
				assert.Equal(t, test.err, got, "keep the complete original cause")
			}
		})
	}
}

func TestForegroundWorkerCancellationBeforeReadiness(t *testing.T) {
	cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
		Mode: lifecycle.RoleWorker, DataDir: cfg.DataDir, Execution: lifecycle.ExecutionForeground,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runWorkerForeground(ctx, runtime, preparedWorkerConfig(t, cfg.DataDir))
	require.ErrorIs(t, err, context.Canceled, "startup must return its actual cancellation cause")
	require.NoError(t, foregroundResult(ctx, err))
	require.NoError(t, runtime.Finish(foregroundResult(ctx, err)))
	record, err := lifecycle.ReadRuntimeRecord(runtime.Paths().RuntimeRecord)
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ProcessStopped, record.Status.Process.State)
	assert.False(t, lockHeld(runtime.Paths()))
}

func TestWorkerForegroundRefusesDifferentConfigurationDirectory(t *testing.T) {
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleWorker, DataDir: t.TempDir(), Execution: lifecycle.ExecutionForeground})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	other := t.TempDir()
	state := []byte("invalid credentials that must not be read")
	require.NoError(t, os.WriteFile(filepath.Join(other, "state.json"), state, 0o600))
	err = runWorkerForeground(t.Context(), runtime, preparedWorkerConfig(t, other))
	require.ErrorContains(t, err, "held data directory")
	unchanged, err := os.ReadFile(filepath.Join(other, "state.json"))
	require.NoError(t, err)
	assert.Equal(t, state, unchanged)
}

func TestForegroundFlagValuesAreNotLifecycleSelectors(t *testing.T) {
	for _, name := range []string{"--data-dir", "--service-spec", "--config"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := false
			err := runModeRunWithInvocation("worker", runInvocation{Context: ctx, OnReady: func() { ready = true; cancel() }})(cmdCtx{}, []string{"--name", name, "--data-dir", cfg.DataDir})
			require.NoError(t, err)
			assert.True(t, ready)
		})
	}
}

func TestForegroundRejectsSelectorsAfterFlagTerminatorBeforePublication(t *testing.T) {
	cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := runModeRunWithInvocation("worker", runInvocation{Context: ctx})(cmdCtx{}, []string{"--", "--data-dir", cfg.DataDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected argument")
	assert.NoFileExists(t, lifecycle.PathsOf(mustCanonicalDir(t, cfg.DataDir)).RuntimeRecord)
}

func TestForegroundAllowsAnEmptyFlagTerminator(t *testing.T) {
	cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := false
	err := runModeRunWithInvocation("worker", runInvocation{Context: ctx, OnReady: func() { ready = true; cancel() }})(cmdCtx{}, []string{"--data-dir", cfg.DataDir, "--"})
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestModePostparseHookPrecedesConfigurationIO(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "malformed.yaml")
			require.NoError(t, os.WriteFile(file, []byte("[invalid YAML"), 0o600))
			sentinel := errors.New("parsed without configuration IO")
			called := false
			_, err := loadLifecycleConfiguration(mode, []string{"--config", file}, nil, func(fs *flag.FlagSet) error {
				called = true
				assert.Equal(t, file, fs.Lookup("config").Value.String())
				return sentinel
			})
			require.ErrorIs(t, err, sentinel)
			assert.True(t, called)
			_, err = loadLifecycleConfiguration(mode, []string{"--config", file}, nil, nil)
			require.Error(t, err, "a real configuration failure must propagate without interception")
		})
	}
}

func TestModePostparseHookFollowsMetadataAndArgumentValidation(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		for _, args := range [][]string{{"--version"}, {"--help"}, {"--unknown-flag"}, {"--", "--data-dir", "ignored"}} {
			t.Run(mode+strings.Join(args, " "), func(t *testing.T) {
				called := false
				_, err := loadLifecycleConfiguration(mode, args, nil, func(*flag.FlagSet) error { called = true; return nil })
				require.Error(t, err)
				assert.False(t, called, "metadata and invalid arguments must precede configuration interception")
			})
		}
		file := filepath.Join(t.TempDir(), "last.yaml")
		sentinel := errors.New("last parsed config selected")
		_, err := loadLifecycleConfiguration(mode, []string{"--config", "first.yaml", "--config", file}, nil, func(fs *flag.FlagSet) error {
			assert.Equal(t, file, fs.Lookup("config").Value.String())
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
	}
}

func TestRunInvocationReportsReadinessAndJoinsParentCancellation(t *testing.T) {
	for _, mode := range []string{"hub", "dev"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			notified := false
			invocation := runInvocation{Context: ctx, OnReady: func() { notified = true; cancel() }}
			err := runModeRunWithInvocation(mode, invocation)(cmdCtx{}, []string{"--data-dir", root, "--listen", locallistentest.UniqueListenURL(t, "lmx-native-ready")})
			require.NoError(t, err)
			assert.True(t, notified)
			record, err := lifecycle.ReadRuntimeRecord(lifecycle.PathsOf(mustCanonicalDir(t, root)).RuntimeRecord)
			require.NoError(t, err)
			assert.Equal(t, lifecycle.ProcessStopped, record.Status.Process.State)
			held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(record.DataDir))
			require.NoError(t, err)
			assert.False(t, held)
		})
	}
}

func TestDesiredSpecResolvesDefaultConfiguration(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			spec, err := desiredSpec(mode, lifecycle.StartupLogin, t.TempDir(), nil)
			require.NoError(t, err)
			assert.True(t, filepath.IsAbs(spec.ConfigFile), "the default config path must be absolute")
			assert.NotContains(t, spec.ConfigFile, "~")
		})
	}
}

func TestDesiredSpecPreservesRuntimeOverrides(t *testing.T) {
	dataDir := t.TempDir()
	configFile := filepath.Join(dataDir, "hub.yaml")
	spec, err := desiredSpec("hub", lifecycle.StartupLogin, dataDir,
		[]string{"--config", configFile, "--log-level", "debug"})
	require.NoError(t, err)
	assert.Equal(t, []string{"--log-level", "debug"}, spec.RuntimeArguments)
}

func TestEnvironmentFilePathOverridesCapturedShellPath(t *testing.T) {
	t.Setenv("PATH", "captured-shell-path")
	path := filepath.Join(t.TempDir(), "environment.yaml")
	providerPath := filepath.ToSlash(filepath.Join(t.TempDir(), "provider-bin"))
	require.NoError(t, os.WriteFile(path, []byte("PATH: '"+providerPath+"'\n"), 0o600))
	spec, err := desiredSpec("hub", lifecycle.StartupBoot, t.TempDir(), []string{"--environment-file", path})
	require.NoError(t, err)
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	require.NoError(t, err)
	environment, err := launch.LoadEnvironment()
	require.NoError(t, err)
	assert.Equal(t, providerPath, environment["PATH"])
}

func TestDesiredSpecResolvesRelativeConfiguration(t *testing.T) {
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	spec, err := desiredSpec("hub", lifecycle.StartupLogin, t.TempDir(),
		[]string{"--config", "relative-hub.yaml"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workingDirectory, "relative-hub.yaml"), spec.ConfigFile)
}

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
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			root := t.TempDir()
			args := []string{"--data-dir", root}
			if mode == "worker" {
				cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
				root = cfg.DataDir
				args = []string{"--data-dir", root}
			} else {
				args = append(args, "--listen", locallistentest.UniqueListenURL(t, "lmx-foreground"))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := false
			invocation := runInvocation{Context: ctx, OnReady: func() { ready = true; cancel() }}
			commands := lifecycleCommandsWithDependencies(mode, lifecycleDependencies{
				foreground: invocation,
				controller: func(string, string) (*servicelaunch.Controller, error) {
					t.Fatal("foreground execution accessed the native controller or elevation path")
					return nil, errors.New("forbidden native access")
				},
			})
			require.Equal(t, "run", commands.Commands[0].Name)
			require.NoError(t, commands.Commands[0].Run(cmdCtx{}, args))
			require.True(t, ready, "exercise successful foreground execution")
			specPath := lifecycle.PathsOf(mustCanonicalDir(t, root)).ServiceSpec
			assert.NoFileExists(t, specPath, "foreground startup never configures native startup")
			require.Error(t, commands.Commands[0].Run(cmdCtx{}, append(args, "--unknown-foreground-option")))
			assert.NoFileExists(t, specPath, "foreground failure never configures native startup")
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
			_, _, err := loadLifecycleCommand("hub", "start", []string{"--data-dir", t.TempDir(), "--timeout", value})
			require.Error(t, err, "timeout %q must fail validation", value)
		}
	})

	t.Run("an overflowing duration fails", func(t *testing.T) {
		_, _, err := loadLifecycleCommand("hub", "start", []string{"--data-dir", t.TempDir(), "--timeout", "999999999999999999999s"})
		require.Error(t, err)
	})

	t.Run("a very large representable duration succeeds", func(t *testing.T) {
		_, options, err := loadLifecycleCommand("hub", "start", []string{"--data-dir", t.TempDir(), "--timeout", "87600h"})
		require.NoError(t, err)
		assert.Equal(t, 87600*time.Hour, options.timeout)
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
		// identifies status, and no replacement launched.
		dataDir := t.TempDir()
		err := waitForReadiness("worker", mustCanonicalDir(t, dataDir), 150*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded, "the command must fail when its readiness wait expires")
	})

	t.Run("a lock held past the shutdown budget reports continuation", func(t *testing.T) {
		dataDir := t.TempDir()
		canonical := mustCanonicalDir(t, dataDir)
		paths := lifecycle.PathsOf(canonical)
		require.NoError(t, atomicfile.MkdirAllDurable(paths.Dir))
		// Write a record and hold the runtime lock in this process.
		require.NoError(t, lifecycle.WriteControlToken(paths.ControlToken, "token-material"))
		require.NoError(t, lifecycle.WriteRuntimeRecord(paths.RuntimeRecord, lifecycle.RuntimeRecord{
			InstanceID: lifecycle.NewInstanceID(lifecycle.CurrentAccountID(), lifecycle.RoleWorker, canonical).String(),
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
			"the refusal identifies what happened: %v", err)
	})
}

func TestReadinessWaitAcceptsTinyPositiveTimeout(t *testing.T) {
	assert.NotPanics(t, func() {
		err := waitForReadiness("worker", mustCanonicalDir(t, t.TempDir()), time.Nanosecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestReadinessAndShutdownContinueAfterWaitTimeout(t *testing.T) {
	root := mustCanonicalDir(t, t.TempDir())
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleHub, DataDir: root, Execution: lifecycle.ExecutionForeground})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	require.ErrorIs(t, waitForReadiness("hub", root, time.Nanosecond), context.DeadlineExceeded)
	runtime.SetReadiness(lifecycle.ReadinessReady)
	require.NoError(t, waitForReadiness("hub", root, time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Stop(ctx, lifecycle.RoleHub, root)
		done <- err
	}()
	<-runtime.StopSignal()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	held, err := lifecycle.ProbeRuntimeLock(runtime.Paths())
	require.NoError(t, err)
	require.True(t, held, "an accepted stop timeout must retain the running instance")
	require.NoError(t, runtime.Finish(nil))
	held, err = lifecycle.ProbeRuntimeLock(runtime.Paths())
	require.NoError(t, err)
	require.False(t, held)
}

// mustCanonicalDir canonicalizes for assertions.
func mustCanonicalDir(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(dir)
	require.NoError(t, err)
	return canonical
}

func TestLifecycleStartupOptions(t *testing.T) {
	for _, verb := range []string{"enable", "start", "restart"} {
		for _, args := range [][]string{{"--startup=boot"}, {"--startup", "boot"}, {"--startup", "login"}, {}} {
			configuration, options, err := loadLifecycleCommand("hub", verb, append([]string{"--data-dir", t.TempDir(), "--log-level", "debug", "--yes"}, args...))
			require.NoError(t, err)
			want := lifecycle.StartupBoot
			if len(args) == 0 {
				want = ""
			} else if args[len(args)-1] == "login" {
				want = lifecycle.StartupLogin
			}
			assert.Equal(t, want, options.startup)
			assert.True(t, options.yes)
			assert.Contains(t, configuration.runtimeArguments, "debug")
			assert.NotContains(t, configuration.runtimeArguments, "--startup")
			assert.NotContains(t, configuration.runtimeArguments, "--yes")
		}
		for _, args := range [][]string{{"--startup=whenever"}, {"--startup="}, {"--startup"}} {
			_, _, err := loadLifecycleCommand("hub", verb, append([]string{"--data-dir", t.TempDir()}, args...))
			require.Error(t, err)
		}
	}
}

func TestLifecycleTimeoutOptions(t *testing.T) {
	for _, verb := range []string{"start", "restart", "stop", "status"} {
		for _, args := range [][]string{{"--timeout=2"}, {"--timeout", "2"}, {"--timeout=2s"}, {"--timeout", "2s"}} {
			_, options, err := loadLifecycleCommand("hub", verb, append([]string{"--data-dir", t.TempDir()}, args...))
			require.NoError(t, err)
			assert.Equal(t, 2*time.Second, options.timeout)
		}
		for _, args := range [][]string{{"--timeout"}, {"--timeout="}} {
			_, _, err := loadLifecycleCommand("hub", verb, append([]string{"--data-dir", t.TempDir()}, args...))
			require.Error(t, err)
		}
	}
}

// An unresponsive instance -- the runtime lock held, the endpoint
// silent -- is a status FAILURE (exit 1), never a stopped instance
// (exit 3): the evidence says the process still exists.
func TestStatusReportsUnresponsiveAsFailureNotStopped(t *testing.T) {
	dataDir := t.TempDir()
	canonical := mustCanonicalDir(t, dataDir)
	paths := lifecycle.PathsOf(canonical)
	require.NoError(t, atomicfile.MkdirAllDurable(paths.Dir))
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
