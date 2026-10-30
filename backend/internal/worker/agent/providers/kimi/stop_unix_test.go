//go:build unix

package kimi

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil/processtest"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/procutil"
)

func TestHelperOwnedProcessTree(*testing.T) { processtest.RunHelper() }

func startKimiOwnedTree(t *testing.T, id string) (*Agent, *processtest.Tree) {
	t.Helper()
	home := t.TempDir()
	tree := processtest.New(t, home)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(ctx, executable, processtest.Arguments()...)
	cmd.Dir = home
	cmd.Env = envutil.PinEnv(os.Environ(), tree.Environment()...)
	procutil.DetachFromTerminal(cmd)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	opts := agent.Options{AgentID: id, WorkingDir: home, APITimeout: 30 * time.Second}
	sink := &agenttest.ControlSink{}
	a := &Agent{
		Process:    providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "kimi"}, pipes, ctx, cancel),
		sink:       agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		workingDir: home,
		clock:      quartz.NewReal(),
	}
	t.Cleanup(func() { cancel(); a.Process.Stop(); _ = a.Process.Wait() })
	require.NoError(t, a.StartCmd())
	a.DrainStderr(pipes.Stderr())
	go a.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func([]byte) {})
	tree.Ready(t, a.Cmd().Process.Pid)
	tree.RequirePong(t)
	return a, tree
}

func connectKimiOwnedTreeSession(t *testing.T, a *Agent, url string) {
	t.Helper()
	opts := agent.Options{AgentID: a.AgentID(), WorkingDir: a.workingDir, APITimeout: 30 * time.Second}
	require.NoError(t, a.connect(a.Context(), url, fakeKapToken, opts, opts.APITimeout))
	t.Cleanup(a.closeStream)
	require.NoError(t, a.openStartupSession(opts, opts.APITimeout))
}

type kimiRootExitResult struct {
	killed bool
	err    error
}

func newKimiRootExitHook(t *testing.T, a *Agent, tree *processtest.Tree) (func(), <-chan kimiRootExitResult) {
	t.Helper()
	rootKilled := make(chan kimiRootExitResult, 1)
	released := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(released) }) })
	return func() {
		killed, err := tree.Root.Kill()
		rootKilled <- kimiRootExitResult{killed: killed, err: err}
		select {
		case <-a.ProcessDone():
		case <-released:
		case <-t.Context().Done():
		}
	}, rootKilled
}

func stopKimiAndAwaitRootExit(t *testing.T, a *Agent, rootExit <-chan kimiRootExitResult) {
	t.Helper()
	stopped := make(chan struct{})
	go func() { a.Stop(); close(stopped) }()
	deadline := testutil.DeadlineContext(t)
	select {
	case result := <-rootExit:
		require.NoError(t, result.err)
		require.True(t, result.killed)
	case <-deadline.Done():
		t.Fatal("the control request did not end the owned root")
	}
	select {
	case <-stopped:
	case <-deadline.Done():
		t.Fatal("stop did not finish after the server exited")
	}
}

func TestKimiStopCapturesDetachedCommandsBeforeServerShutdown(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, tree := startKimiOwnedTree(t, "owned-kimi-stop")
	_, unrelated := startKimiOwnedTree(t, "unrelated-kimi-stop")
	connectKimiOwnedTreeSession(t, a, server.URL)
	a.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}}))
	beforeReply, rootExit := newKimiRootExitHook(t, a, tree)
	fake.reply("POST "+kimiRouteShutdown, fakeKapReply{BeforeReply: beforeReply})
	stopKimiAndAwaitRootExit(t, a, rootExit)
	unrelated.RequirePong(t)
	tree.RequireChildClosed(t)
	routes := fake.routes()
	abortAt, shutdownAt := -1, -1
	for index, route := range routes {
		if route == "POST "+kimiSessionPath("session_1", kimiActionAbort) {
			abortAt = index
		}
		if route == "POST "+kimiRouteShutdown {
			shutdownAt = index
		}
	}
	require.GreaterOrEqual(t, abortAt, 0)
	require.Greater(t, shutdownAt, abortAt)
	assert.True(t, a.IsStopped())
	a.Stop()
	assert.Equal(t, routes, fake.routes(), "a second stop sends no additional requests")
}

func TestKimiStopSendsNoRequestsAfterNativeExit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		wait  bool
	}{
		{label: "before provider wait"},
		{label: "after provider wait", wait: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			fake, server := newFakeKap(t)
			a, tree := startKimiOwnedTree(t, "exited-kimi-stop")
			connectKimiOwnedTreeSession(t, a, server.URL)
			a.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}}))
			require.NoError(t, a.BindDescendants(t.Context()))
			killed, err := tree.Root.Kill()
			require.NoError(t, err)
			require.True(t, killed)
			select {
			case <-a.ProcessDone():
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the native root did not finish")
			}
			if tc.wait {
				require.Error(t, a.Wait())
			}
			before := fake.routes()
			a.Stop()
			assert.Equal(t, before, fake.routes(), "an exited instance must not control a server at its former endpoint")
			assert.True(t, a.IsStopped())
			tree.RequireChildClosed(t)
		})
	}
}

func TestKimiStopRetainsCommandsWhenTheServerExitsDuringAbort(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, tree := startKimiOwnedTree(t, "abort-exit-kimi-stop")
	connectKimiOwnedTreeSession(t, a, server.URL)
	a.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}}))
	beforeReply, rootExit := newKimiRootExitHook(t, a, tree)
	fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{BeforeReply: beforeReply})
	stopKimiAndAwaitRootExit(t, a, rootExit)
	assert.Empty(t, fake.requestsTo("POST "+kimiRouteShutdown), "an abort that ends the instance must prevent a later shutdown request")
	tree.RequireChildClosed(t)
}
