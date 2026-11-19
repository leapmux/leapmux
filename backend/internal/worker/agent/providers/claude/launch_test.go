package claude

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude's registration states no resolver, so a launch resolves the name that
// its locator lists: the same list that the availability scan probes. Passing the
// names in would be a second source that could disagree with it.
func TestClaudeLaunchFallsBackToItsRegisteredBinaryName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX command fixture")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	reg := Registration()
	spec, err := providerkit.ResolveLaunch(context.Background(), agent.Options{Shell: shell}, reg)

	require.NoError(t, err)
	directory := t.TempDir()
	testutil.WriteExecutable(t, filepath.Join(directory, "claude"), "#!/bin/sh\nprintf 'registered claude\\n'\n")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	prepared, err := launch.Wrap(t.Context(), launch.WrapSpec{Shell: shell, Launch: spec})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	output, err := prepared.Cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "registered claude")
	assert.Empty(t, spec.PrefixArgs)
	assert.Empty(t, spec.Env)
}

const blockedProfileAddressEnv = "LEAPMUX_TEST_CLAUDE_BLOCKED_PROFILE"

func TestClaudeBlockedProfilePeer(t *testing.T) {
	address := os.Getenv(blockedProfileAddressEnv)
	if address == "" {
		return
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		os.Exit(42)
	}
	if _, err := fmt.Fprintln(conn, os.Getpid()); err != nil {
		os.Exit(43)
	}
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
	os.Exit(0)
}

func TestClaudeStartupDeadlineStopsABlockedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated Bash profile")
	}
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	listener := agenttest.ListenForProcessPeer(t, 15*time.Second)
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	file := filepath.Join(dir, "profile")
	require.NoError(t, os.WriteFile(file, []byte(shellquote.POSIX.Quote(executable)+" -test.run=^TestClaudeBlockedProfilePeer$\n"), 0o600))
	t.Setenv("BASH_ENV", file)
	t.Setenv(blockedProfileAddressEnv, listener.Addr().String())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() {
		started, err := Start(ctx, agent.Options{
			AgentID: "blocked-profile", HomeDir: dir, WorkingDir: dir, Shell: shell,
			StartupTimeout: 3 * time.Second, APITimeout: 30 * time.Second,
		}, agent.NewProviderServices(&agenttest.Sink{}))
		if started != nil {
			started.Stop()
			_ = started.Wait()
		}
		outcome <- err
	}()
	identity, conn := agenttest.AcceptProcessPeer(t, listener, 15*time.Second)
	t.Cleanup(func() { _, _ = identity.Kill() })
	select {
	case err := <-outcome:
		require.ErrorIs(t, err, errControlTimeout)
		assert.ErrorContains(t, err, "initialize")
	case <-ctx.Done():
		t.Fatal("startup did not return after the configured deadline")
	}
	agenttest.RequireProcessPeerClosed(t, conn)
}
