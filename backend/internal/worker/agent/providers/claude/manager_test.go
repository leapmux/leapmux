//go:build unix

package claude

import (
	"bufio"
	"context"
	"os/exec"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startMockAgent wraps mockStart to satisfy the StartFunc signature.
func startMockAgent(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return mockStart(ctx, opts, sink)
}

func TestManager_SetOnExit_FiresOnStop(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, func(string, int, error, bool) {
		// Original handler: should be replaced by SetOnExit below.
		t.Error("original onExit should not be called after SetOnExit")
	})

	exited := make(chan string, 1)
	m.SetOnExit(func(agentID string, _ int, _ error, _ bool) {
		exited <- agentID
	})

	_, err := m.StartAgentWith(context.Background(), agent.Options{
		AgentID:    "s-exit",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err)

	m.StopAgent("s-exit")

	select {
	case got := <-exited:
		assert.Equal(t, "s-exit", got)
	case <-time.After(2 * time.Second):
		t.Fatal("exit handler did not fire after stop")
	}
}

func TestManager_StartAndStop(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	_, err := m.StartAgentWith(ctx, agent.Options{
		AgentID:    "s1",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "StartAgent")

	assert.True(t, m.HasAgent("s1"), "expected HasAgent(s1) = true")

	// Duplicate start should fail.
	_, err = m.StartAgentWith(ctx, agent.Options{
		AgentID:    "s1",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	assert.Error(t, err, "expected error for duplicate agent")

	// Stop and verify cleanup.
	m.StopAgent("s1")

	// Wait for the background goroutine to clean up.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("s1")
	}, "expected HasAgent(s1) = false after stop")
}

func TestManager_SendInput(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()
	sink := &agenttest.Sink{}

	_, err := m.StartAgentWith(ctx, agent.Options{
		AgentID:    "s2",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(sink), startMockAgent)
	require.NoError(t, err, "StartAgent")
	defer m.StopAgent("s2")

	// SendInput sends a user JSON message; the mock echoes it back.
	// Since it's a simple user text echo, HandleOutput drops it.
	// Send raw assistant NDJSON to verify the full pipeline.
	require.NoError(t, m.SendRawInput("s2", []byte(`{"type":"assistant","message":{"role":"assistant","content":"hi"}}`+"\n")), "SendRawInput")

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected output from agent")
}

// A message that arrives while the agent restarts must wait for the new
// process, not fail against the old one.
//
// The window is a settings change the provider cannot apply live: the service
// takes the lifecycle lock, stops the old process, and starts a new one. A send
// that lands in it must not reach the stopped process and fail the queue item.
func TestManager_SendInputWaitsForRestartToFinish(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()
	const agentID = "s-send-during-restart"
	opts := agent.Options{
		AgentID:    agentID,
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}

	_, err := m.StartAgentWith(ctx, opts, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "StartAgent")

	// Open the restart window by hand -- lock held, old process gone, new one not
	// started -- which is what RestartAgent holds across its stop and its start.
	unlock := m.LockAgent(agentID)
	require.True(t, m.StopAndWaitAgent(agentID), "expected the running agent to stop")

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- m.SendInput(agentID, "sent while the agent was restarting", nil)
	}()

	// Nothing can deliver this yet, so the send must still be in flight. A send
	// that resolves here resolved against no process at all, which is the bug.
	select {
	case err := <-sendErr:
		t.Fatalf("send resolved inside the restart window: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Finish the restart, then release the lock the way the service does.
	_, err = m.StartAgentWith(ctx, opts, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "restart")
	unlock()
	defer m.StopAgent(agentID)

	select {
	case err := <-sendErr:
		assert.NoError(t, err, "the waiting send must land on the restarted agent")
	case <-time.After(5 * time.Second):
		t.Fatal("send never completed after the restart finished")
	}
}

// A message to a SUBAGENT tab reaches the owner process, so it meets the same
// restart window a root send does -- and used to fail the same way, with the
// child's queue item could fail for a restart that the user did not request.
// Each input path into the process must wait for the restart.
func TestManager_SendChildInputWaitsForRestartToFinish(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()
	const agentID = "s-child-send-during-restart"
	opts := agent.Options{
		AgentID:    agentID,
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}

	_, err := m.StartAgentWith(ctx, opts, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "StartAgent")

	// The restart window, held open by hand the way RestartAgent holds it.
	unlock := m.LockAgent(agentID)
	require.True(t, m.StopAndWaitAgent(agentID), "expected the running agent to stop")

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- m.SendChildInput(agentID, "child-key", "sent while restarting", nil)
	}()

	// Nothing can deliver this yet. A send that resolves here resolved against
	// no process at all, which is the bug.
	select {
	case err := <-sendErr:
		t.Fatalf("child send resolved inside the restart window: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	_, err = m.StartAgentWith(ctx, opts, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "restart")
	unlock()
	defer m.StopAgent(agentID)

	select {
	case err := <-sendErr:
		// The mock provider steers no child, so the ERROR is expected. What
		// matters is that the call got past the lock to a live process at all,
		// rather than resolving against the one the restart was destroying.
		assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
	case <-time.After(5 * time.Second):
		t.Fatal("child send never completed after the restart finished")
	}
}

// The lifecycle lock covers the map READ, never the provider write.
//
// A provider's SendInput blocks for as long as the turn takes -- Codex's
// turn/start has no timeout at all. Holding a lifecycle lock across that would
// park every restart, /clear, plan execution and auto-start for the agent
// behind one message, and would defeat mid-turn steering, which needs a second
// send to reach the provider WHILE the first turn runs.
func TestManager_SendInputDoesNotHoldTheLifecycleLockAcrossTheWrite(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()
	const agentID = "s-send-releases-lock"

	release := make(chan struct{})
	entered := make(chan struct{})
	start := func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
		base, err := mockStart(ctx, opts, sink)
		if err != nil {
			return nil, err
		}
		return &blockingSendAgent{Agent: base, entered: entered, release: release}, nil
	}
	_, err := m.StartAgentWith(ctx, agent.Options{
		AgentID:    agentID,
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), start)
	require.NoError(t, err, "StartAgent")

	sendDone := make(chan error, 1)
	go func() { sendDone <- m.SendInput(agentID, "a message that blocks", nil) }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the provider write never started")
	}

	// The write is in flight. A lifecycle operation must still be able to take
	// the lock -- that is the whole difference between a slow send and a wedged
	// agent.
	locked := make(chan struct{})
	go func() {
		unlock := m.LockAgent(agentID)
		close(locked)
		unlock()
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("a lifecycle operation blocked behind an in-flight send")
	}

	close(release)
	require.NoError(t, <-sendDone)
}

func TestManager_StopAll(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c"} {
		_, err := m.StartAgentWith(ctx, agent.Options{
			AgentID:    id,
			Options:    map[string]string{agent.OptionIDModel: "test"},
			WorkingDir: t.TempDir(),
		}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
		require.NoError(t, err, "StartAgent(%s)", id)
	}

	m.StopAll()

	// Wait for background cleanup goroutines.
	for _, id := range []string{"a", "b", "c"} {
		id := id
		testutil.AssertEventually(t, func() bool {
			return !m.HasAgent(id)
		}, "HasAgent(%s) = true after StopAll", id)
	}
}

func TestManager_StopAndWaitAgent(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	_, err := m.StartAgentWith(ctx, agent.Options{
		AgentID:    "s1",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "StartAgent")

	// StopAndWaitAgent should block until the agent is fully removed.
	assert.True(t, m.StopAndWaitAgent("s1"), "expected StopAndWaitAgent to return true")
	assert.False(t, m.HasAgent("s1"), "expected HasAgent(s1) = false immediately after StopAndWaitAgent")

	// A new agent with the same ID should start successfully.
	_, err = m.StartAgentWith(ctx, agent.Options{
		AgentID:    "s1",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err, "StartAgent after StopAndWaitAgent should succeed")
	m.StopAgent("s1")
}

func TestManager_LockAgent_ComposesStopAndStart(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	_, err := m.StartAgentWith(ctx, agent.Options{AgentID: "r1", Options: map[string]string{agent.OptionIDModel: "test"}, WorkingDir: t.TempDir()}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err)

	unlock := m.LockAgent("r1")
	m.StopAndWaitAgent("r1")
	_, err = m.StartAgentWith(ctx, agent.Options{AgentID: "r1", Options: map[string]string{agent.OptionIDModel: "test"}, WorkingDir: t.TempDir()}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	unlock()
	require.NoError(t, err, "restart composed under LockAgent should succeed")
	assert.True(t, m.HasAgent("r1"))
	m.StopAgent("r1")
}

func TestManager_LockAgent_SerializesConcurrentRestarts(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	_, err := m.StartAgentWith(ctx, agent.Options{AgentID: "race", Options: map[string]string{agent.OptionIDModel: "test"}, WorkingDir: t.TempDir()}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
	require.NoError(t, err)

	restart := func() error {
		unlock := m.LockAgent("race")
		defer unlock()
		m.StopAndWaitAgent("race")
		_, err := m.StartAgentWith(ctx, agent.Options{AgentID: "race", Options: map[string]string{agent.OptionIDModel: "test"}, WorkingDir: t.TempDir()}, agent.NewProviderServices(agenttest.Nop{}), startMockAgent)
		return err
	}

	errCh := make(chan error, 2)
	go func() { errCh <- restart() }()
	go func() { errCh <- restart() }()

	// Both must succeed: the lock serializes them so neither trips the
	// "agent already running" guard.
	assert.NoError(t, <-errCh)
	assert.NoError(t, <-errCh)
	m.StopAgent("race")
}

func TestManager_AgentExitCleanup(t *testing.T) {
	m := agent.NewManager(claudeTestRegistry, nil)
	ctx := context.Background()

	// Start an agent that will exit on its own when stdin is closed.
	_, err := m.StartAgentWith(ctx, agent.Options{
		AgentID:    "auto-exit",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(agenttest.Nop{}), func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
		// Create a process that exits immediately.
		ctx2, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(ctx2, "true")
		cmd.Dir = opts.WorkingDir

		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = nil

		a := &Agent{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID:     opts.AgentID,
				Cmd:         cmd,
				Stdin:       stdin,
				Ctx:         ctx2,
				Cancel:      cancel,
				ProcessDone: make(chan struct{}),
				StderrDone:  make(chan struct{}),
			}),
			model:          opts.Model(),
			workingDir:     opts.WorkingDir,
			sink:           sink,
			pendingControl: make(map[string]chan<- claudeCodeControlResult),
		}
		a.SkipStderr()

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutputLoop(scanner)
		return a, nil
	})
	require.NoError(t, err, "StartAgent")

	// Wait for the process to exit and cleanup to happen.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("auto-exit")
	}, "expected agent to be cleaned up after exit")
}

// blockingSendAgent parks in SendInput until it is released, so a test can hold
// a provider write open and inspect what the manager does meanwhile. Every
// other method is the wrapped mock's.
type blockingSendAgent struct {
	agent.Agent
	entered chan struct{}
	release chan struct{}
}

func (a *blockingSendAgent) SendInput(string, []*leapmuxv1.Attachment) error {
	close(a.entered)
	<-a.release
	return nil
}
