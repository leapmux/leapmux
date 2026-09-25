//go:build unix

package kimi

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestDescendantGroupsFromTable(t *testing.T) {
	t.Parallel()

	// pid ppid pgid, as `ps -A -o pid= -o ppid= -o pgid=` prints it.
	const table = `
    1     0     1
  100     1   100
  200   100   100
  300   200   300
  301   300   300
  400   200   400
  500     1   500
  600   400    50
  700   300     1
  bad  line  here
  800
`
	groups := descendantGroupsFromTable([]byte(table), 200, 100)
	assert.ElementsMatch(t, []int{300, 400, 50}, groups,
		"every group under the server, minus the worker's own and init's")

	assert.Empty(t, descendantGroupsFromTable([]byte(table), 999, 100), "a root the table lacks has no descendants")
	assert.Empty(t, descendantGroupsFromTable(nil, 200, 100))

	// A cycle in a corrupt table ends.
	cycle := "10 20 10\n20 10 20\n"
	assert.ElementsMatch(t, []int{10, 20}, descendantGroupsFromTable([]byte(cycle), 10, 0))
}

func TestKimiDescendantGroupsReadsTheLiveTable(t *testing.T) {
	t.Parallel()

	child := exec.Command("sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	// The test process is the root: the child is its own group, the way the
	// server starts each tool command.
	groups := kimiDescendantGroups(syscall.Getpid())
	assert.Contains(t, groups, child.Process.Pid)
	assert.NotContains(t, groups, syscall.Getpgrp(), "the worker's own group is never a target")
}

// startDetachedSleeper starts a process in a group of its own, as the server
// starts a tool command, and reports when it exits.
func startDetachedSleeper(t *testing.T) (*exec.Cmd, <-chan *exec.ExitError) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	exited := make(chan *exec.ExitError, 1)
	go func() {
		err := cmd.Wait()
		exitErr, _ := err.(*exec.ExitError)
		exited <- exitErr
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, exited
}

func TestKimiStopEndsTheServerAndTheCommandsItLeft(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)

	// The "server" process: a sleeper that /shutdown ends, as the real one does.
	serverCmd, serverExited := startDetachedSleeper(t)
	processDone := make(chan struct{})
	var once sync.Once
	fake.mu.Lock()
	fake.onShutdown = func() { _ = serverCmd.Process.Kill() }
	fake.mu.Unlock()
	go func() {
		<-serverExited
		once.Do(func() { close(processDone) })
	}()

	// A tool command the server started, which outlives the server.
	orphan, orphanExited := startDetachedSleeper(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sink := &agenttest.ControlSink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "kimi", Cmd: serverCmd, Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(io.Discard), ProcessDone: processDone, APITimeout: 30 * time.Second,
		}),
		sink:       agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		workingDir: "/work/project",
		clock:      quartz.NewReal(),
	}
	var askedRoot int
	a.descendantGroups = func(rootPID int) []int {
		askedRoot = rootPID
		return []int{orphan.Process.Pid}
	}
	opts := agent.Options{AgentID: "test-agent", APITimeout: 30 * time.Second}
	require.NoError(t, a.connect(ctx, server.URL, fakeKapToken, opts, 30*time.Second))
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	a.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}}))

	a.Stop()

	routes := fake.routes()
	abortAt, shutdownAt := -1, -1
	for i, route := range routes {
		switch route {
		case "POST " + kimiSessionPath("session_1", kimiActionAbort):
			abortAt = i
		case "POST " + kimiRouteShutdown:
			shutdownAt = i
		}
	}
	require.GreaterOrEqual(t, abortAt, 0, "the running turn is aborted, which kills its command")
	assert.Less(t, abortAt, shutdownAt, "the abort comes before the shutdown")
	assert.Equal(t, serverCmd.Process.Pid, askedRoot, "the groups are listed under the server's own process")

	select {
	case exitErr := <-orphanExited:
		require.NotNil(t, exitErr)
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		require.True(t, ok)
		assert.Equal(t, syscall.SIGKILL, status.Signal(), "a command that outlived the server is killed")
	case <-time.After(30 * time.Second):
		t.Fatal("the orphaned command survived the stop")
	}
	assert.True(t, a.IsStopped())

	// A second stop sends nothing more.
	before := len(fake.routes())
	a.Stop()
	assert.Len(t, fake.routes(), before)
}

func TestKillKimiGroupsSparesWhatItMustNotKill(t *testing.T) {
	t.Parallel()

	// Neither init's group, nor an invalid one, nor the worker's own: a kill of
	// any of them would reach far more than the server's commands. The call
	// returning with this test process alive is the assertion.
	killKimiGroups([]int{0, 1, -5, syscall.Getpgrp()})
	killKimiGroups(nil)

	target, exited := startDetachedSleeper(t)
	killKimiGroups([]int{target.Process.Pid})
	select {
	case exitErr := <-exited:
		require.NotNil(t, exitErr)
	case <-time.After(30 * time.Second):
		t.Fatal("the group survived")
	}
	// A group that is already gone is not an error.
	killKimiGroups([]int{target.Process.Pid})
}
