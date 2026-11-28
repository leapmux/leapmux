package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// The `leapmux` binary serves control commands, so a solo or dev run states
// its own executable for the Worker that it starts. That Worker links it for
// the processes that it spawns.
func TestCombinedRunStatesItsOwnExecutableAsTheControlCLI(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, executable, controlCLIExecutable())

	for _, mode := range []string{"solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			fs := pflag.NewFlagSet("leapmux "+mode+" run", pflag.ContinueOnError)
			fs.SetOutput(io.Discard)
			flags, err := registerLifecycleFlags(mode, "run", fs)
			require.NoError(t, err)
			require.NoError(t, fs.Parse([]string{"--data-dir", t.TempDir()}))

			_, _, err = flags.load(fs)
			require.NoError(t, err)

			assert.Equal(t, executable, flags.soloConfig.ControlCLI)
		})
	}
}

// A worker run states its own executable as well: while the Worker runs, its
// data directory holds one link to this binary for the processes that it
// spawns, and the link goes when the Worker stops.
//
// Not parallel: the worker sets the process-global log level.
func TestWorkerRunLinksItsOwnExecutableAsTheControlCLI(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	linkName := "leapmux"
	if runtime.GOOS == "windows" {
		linkName = "leapmux.exe"
	}
	// The hub accepts the connection and never answers, so the Worker stays
	// up, connecting, until the test stops it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	cfg := writeRegisteredStateAgainst(t, "http://"+listener.Addr().String())
	rt, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleWorker, DataDir: cfg.DataDir, Execution: lifecycle.ExecutionForeground})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWorkerForeground(ctx, rt, preparedWorkerConfig(t, cfg.DataDir)) }()
	select {
	case <-rt.Ready():
	case <-ctx.Done():
		t.Fatal("local worker initialization did not finish")
	}

	pattern := filepath.Join(mustCanonicalDir(t, cfg.DataDir), "run", "cli-*", linkName)
	links, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.Len(t, links, 1, "a running Worker holds one link")
	linkInfo, err := os.Stat(links[0])
	require.NoError(t, err)
	executableInfo, err := os.Stat(executable)
	require.NoError(t, err)
	// A hard link and a symbolic link are the same file, and a copy has the
	// same size; each one runs this binary.
	assert.Equal(t, executableInfo.Size(), linkInfo.Size(), "the link stands for the executable of this process")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the worker did not stop after the cancellation")
	}
	require.NoError(t, rt.Shutdown())
	assert.NoFileExists(t, links[0], "the Worker removes its link when it stops")
}
