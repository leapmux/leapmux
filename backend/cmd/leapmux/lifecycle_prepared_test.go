package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestForegroundConsumesPreparedConfigurationSnapshot(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if mode == "worker" {
				root = writeRegisteredStateAgainst(t, "http://127.0.0.1:1").DataDir
			}
			path := filepath.Join(t.TempDir(), "configuration.yaml")
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("listen:\n  - %q\nlog_level: error\n", locallistentest.UniqueListenURL(t, "snapshot"))), 0o600))
			configuration, err := loadLifecycleConfiguration(mode, "run", []string{"--config", path, "--data-dir", root}, nil, nil)
			require.NoError(t, err)
			replacement := []byte("[malformed after preparation")
			require.NoError(t, os.WriteFile(path, replacement, 0o600))
			runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: modeRole(mode), DataDir: configuration.dataDirectory, Execution: lifecycle.ExecutionForeground})
			require.NoError(t, err)
			t.Cleanup(func() { _ = runtime.Finish(nil) })
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			observed := make(chan struct{})
			go func() {
				defer close(observed)
				select {
				case <-runtime.Ready():
					cancel()
				case <-ctx.Done():
				}
			}()
			err = foregroundResult(ctx, configuration.executeForeground(ctx, runtime))
			cancel()
			<-observed
			require.NoError(t, err, "execution must consume its prepared values rather than reopening changed YAML")
			current, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, replacement, current)
		})
	}
}

func preparedWorkerConfig(t *testing.T, directory string) *workerconfig.Config {
	t.Helper()
	cfg, _, err := workerconfig.Load([]string{"--data-dir", directory})
	require.NoError(t, err)
	return cfg
}

func preparedHubConfig(t *testing.T, args []string) *hubconfig.Config {
	t.Helper()
	cfg, _, err := hubconfig.Load(args)
	require.NoError(t, err)
	return cfg
}

func runWorkerCommandForTest(t *testing.T, args []string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	return runModeRunWithInvocation("worker", runInvocation{Context: ctx})(cmdCtx{}, args)
}

func TestPreparedConfigurationPinsCanonicalDirectoryAfterAliasRetarget(t *testing.T) {
	for _, mode := range []string{"hub", "worker"} {
		t.Run(mode, func(t *testing.T) {
			first, second := t.TempDir(), t.TempDir()
			if mode == "worker" {
				firstCfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
				secondCfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:2")
				first, second = firstCfg.DataDir, secondCfg.DataDir
				state, err := secondCfg.LoadState()
				require.NoError(t, err)
				state.WorkerID = "second-worker"
				require.NoError(t, secondCfg.SaveState(state))
			}
			alias := filepath.Join(t.TempDir(), "selected")
			require.NoError(t, os.Symlink(first, alias))
			args := []string{"--data-dir", alias}
			if mode == "hub" {
				args = append(args, "--listen", locallistentest.UniqueListenURL(t, "alias"))
			}
			configuration, err := loadLifecycleConfiguration(mode, "run", args, nil, nil)
			require.NoError(t, err)
			require.NoError(t, os.Remove(alias))
			require.NoError(t, os.Symlink(second, alias))
			if mode == "worker" {
				registration, err := configuration.readRegistration()
				require.NoError(t, err)
				require.NotNil(t, registration.WorkerID)
				assert.Equal(t, "worker-1", *registration.WorkerID, "observation must read the leased canonical directory")
			}
			runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: modeRole(mode), DataDir: configuration.dataDirectory, Execution: lifecycle.ExecutionForeground})
			require.NoError(t, err)
			t.Cleanup(func() { _ = runtime.Finish(nil) })
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				select {
				case <-runtime.Ready():
					cancel()
				case <-ctx.Done():
				}
			}()
			err = foregroundResult(ctx, configuration.executeForeground(ctx, runtime))
			cancel()
			<-done
			require.NoError(t, err)
			assert.NoFileExists(t, filepath.Join(second, mode+".db"))
		})
	}
}
