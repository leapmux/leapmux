package agent

import (
	"bufio"
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patchStartForTest replaces the Start function behavior by providing
// a function that creates agents using the test helper process.
func startMockAgent(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
	return mockStart(ctx, opts, outputFn)
}

func TestManager_StartAndStop(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, startMockAgent)
	require.NoError(t, err, "StartAgent")

	assert.True(t, m.HasAgent("s1"), "expected HasAgent(s1) = true")

	// Duplicate start should fail.
	_, err = m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, startMockAgent)
	assert.Error(t, err, "expected error for duplicate agent")

	// Stop and verify cleanup.
	m.StopAgent("s1")

	// Wait for the background goroutine to clean up.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("s1")
	}, "expected HasAgent(s1) = false after stop")
}

func TestManager_SendInput(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	var mu sync.Mutex
	var lines []string

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s2",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func(line []byte) {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
	}, startMockAgent)
	require.NoError(t, err, "StartAgent")
	defer m.StopAgent("s2")

	require.NoError(t, m.SendInput("s2", "test message"), "SendInput")

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0
	}, "expected output from agent")
}

func TestManager_SendInputUnknownAgent(t *testing.T) {
	m := NewManager(nil)

	assert.Error(t, m.SendInput("nonexistent", "hello"), "expected error for unknown agent")
}

func TestManager_StopAll(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c"} {
		_, err := m.startAgentWith(ctx, Options{
			AgentID:    id,
			Model:      "test",
			WorkingDir: t.TempDir(),
		}, func([]byte) {}, startMockAgent)
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

func TestManager_StopUnknownAgent(t *testing.T) {
	m := NewManager(nil)
	// Should not panic.
	m.StopAgent("nonexistent")
}

func TestManager_AgentExitCleanup(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	// Start an agent that will exit on its own when stdin is closed.
	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "auto-exit",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, func(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
		// Create a process that exits immediately.
		ctx2, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(ctx2, "true")
		cmd.Dir = opts.WorkingDir

		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = nil

		a := &Agent{
			agentID:        opts.AgentID,
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			cmd:            cmd,
			stdin:          stdin,
			ctx:            ctx2,
			cancel:         cancel,
			processDone:    make(chan struct{}),
			pendingControl: make(map[string]chan<- controlResult),
		}

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutput(scanner, outputFn)
		return a, nil
	})
	require.NoError(t, err, "StartAgent")

	// Wait for the process to exit and cleanup to happen.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("auto-exit")
	}, "expected agent to be cleaned up after exit")
}
