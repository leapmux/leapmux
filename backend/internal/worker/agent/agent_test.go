package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperProcess is a test helper that acts as a mock Claude process.
// It reads stdin lines and echoes them back as NDJSON output.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	// Simple echo: read stdin lines and write them to stdout.
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		_, _ = os.Stdout.Write(buf[:n])
	}
	os.Exit(0)
}

// mockStart spawns a test helper process instead of the real claude binary.
func mockStart(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	cmd.Dir = opts.WorkingDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	cmd.Stderr = nil

	a := &Agent{
		agentID:        opts.AgentID,
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		cmd:            cmd,
		stdin:          stdin,
		ctx:            ctx,
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
}

func TestAgent_StartAndStop(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	var lines []string

	outputFn := func(line []byte) {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
	}

	agent, err := mockStart(ctx, Options{
		AgentID:    "test-workspace",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, outputFn)
	require.NoError(t, err, "mockStart")

	// Send input and verify it's echoed back.
	require.NoError(t, agent.SendInput("hello world"), "SendInput")

	// Wait for the echo.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0
	}, "expected at least one output line")

	// Stop the agent.
	agent.Stop()

	if err := agent.Wait(); err != nil {
		// Context cancellation causes a non-nil exit error; that's expected.
		t.Logf("agent exited with: %v (expected)", err)
	}

	// Verify double-stop is safe.
	agent.Stop()
}

func TestAgent_SendInputAfterStop(t *testing.T) {
	ctx := context.Background()

	agent, err := mockStart(ctx, Options{
		AgentID:    "test-workspace-2",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func([]byte) {})
	require.NoError(t, err, "mockStart")

	agent.Stop()
	_ = agent.Wait()

	assert.Error(t, agent.SendInput("should fail"), "expected error sending input after stop")
}

func TestAgent_AgentID(t *testing.T) {
	ctx := context.Background()

	agent, err := mockStart(ctx, Options{
		AgentID:    "my-agent",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, func([]byte) {})
	require.NoError(t, err, "mockStart")
	defer agent.Stop()

	assert.Equal(t, "my-agent", agent.AgentID())
}

func TestAgent_WorkingDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Create a marker file in the temp dir.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("ok"), 0o644))

	agent, err := mockStart(ctx, Options{
		AgentID:    "wd-test",
		Model:      "test",
		WorkingDir: dir,
	}, func([]byte) {})
	require.NoError(t, err, "mockStart")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	// The process started in dir — this is verified by the mock starting successfully.
	assert.Equal(t, dir, agent.workingDir)
}

// TestHelperProcessWithInit is a test helper that acts as a mock Claude process
// that outputs an init message with a session_id on startup, then echoes stdin.
func TestHelperProcessWithInit(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_WITH_INIT") != "1" {
		return
	}

	// Write init message immediately.
	fmt.Println(`{"type":"system","subtype":"init","session_id":"test-session-abc123"}`)
	_ = os.Stdout.Sync()

	// Then echo stdin.
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		_, _ = os.Stdout.Write(buf[:n])
	}
	os.Exit(0)
}

// mockStartWithInit spawns a test helper process that outputs an init line
// with a session_id, simulating real Claude Code behavior. Output flows
// immediately to outputFn without gating.
func mockStartWithInit(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcessWithInit", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS_WITH_INIT=1")
	cmd.Dir = opts.WorkingDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	cmd.Stderr = nil

	a := &Agent{
		agentID:        opts.AgentID,
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		cmd:            cmd,
		stdin:          stdin,
		ctx:            ctx,
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
}

// TestAgent_InitMessageFlowsThrough verifies that the init message with
// session_id is forwarded to outputFn. Session ID extraction now happens
// on the hub side in HandleAgentOutput, not in the agent process.
func TestAgent_InitMessageFlowsThrough(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	var lines []string
	outputFn := func(line []byte) {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
	}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "init-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, outputFn)
	require.NoError(t, err, "mockStartWithInit")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	// The init message should be forwarded through outputFn.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) >= 1
	}, "expected init message to be forwarded")

	mu.Lock()
	assert.Contains(t, lines[0], "test-session-abc123",
		"first forwarded line should be the init message with session_id")
	mu.Unlock()

	// Send additional input and verify it flows through too.
	require.NoError(t, agent.SendInput("hello"))
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) >= 2
	}, "expected additional output after input")
}

// TestHelperProcessEarlyExit is a test helper that writes an error to stderr
// and exits immediately, simulating Claude Code detecting CLAUDECODE env var.
func TestHelperProcessEarlyExit(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_EARLY_EXIT") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "Error: Claude Code cannot be launched inside another Claude Code session")
	os.Exit(1)
}

// TestHelperProcessUnresponsive is a test helper that reads stdin but never
// produces any output, simulating a hung agent.
func TestHelperProcessUnresponsive(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNRESPONSIVE") != "1" {
		return
	}
	// Read stdin forever but never write anything.
	buf := make([]byte, 4096)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
	}
	os.Exit(0)
}

func TestAgent_StartTimeoutCleansUpProcess(t *testing.T) {
	ctx := context.Background()

	// Use mock infra to test the timeout path: a process that reads stdin
	// but never writes a control_response, causing the handshake to timeout.
	startUnresponsive := func(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
		ctx2, cancel := context.WithCancel(ctx)

		cmd := exec.CommandContext(ctx2, os.Args[0], "-test.run=TestHelperProcessUnresponsive", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS_UNRESPONSIVE=1")
		cmd.Dir = opts.WorkingDir

		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, err
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, err
		}

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

		// Replicate the startup handshake from Start().
		mode := opts.PermissionMode
		if mode == "" {
			mode = "default"
		}
		requestID := generateRequestID()
		ch := make(chan controlResult, 1)
		a.registerPendingControl(requestID, ch)

		msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":{"subtype":"set_permission_mode","mode":"%s"}}`,
			requestID, mode)
		if sendErr := a.SendRawInput([]byte(msg)); sendErr != nil {
			a.unregisterPendingControl(requestID)
			a.Stop()
			_ = a.Wait()
			return nil, sendErr
		}

		cleanup := func() {
			a.Stop()
			_ = a.Wait()
		}

		select {
		case resp := <-ch:
			a.unregisterPendingControl(requestID)
			if !resp.Success {
				cleanup()
				return nil, fmt.Errorf("set_permission_mode failed: %s", resp.Error)
			}
			a.confirmedPermissionMode = resp.Mode
		case <-ctx2.Done():
			a.unregisterPendingControl(requestID)
			cleanup()
			return nil, ctx2.Err()
		case <-time.After(opts.startupTimeout()):
			a.unregisterPendingControl(requestID)
			cleanup()
			return nil, fmt.Errorf("timeout waiting for agent to respond")
		}

		return a, nil
	}

	agent, err := startUnresponsive(ctx, Options{
		AgentID:        "timeout-test-mock",
		Model:          "test",
		WorkingDir:     t.TempDir(),
		StartupTimeout: 200 * time.Millisecond,
	}, func([]byte) {})

	assert.Nil(t, agent, "agent should be nil on timeout")
	require.Error(t, err, "expected timeout error")
	assert.Contains(t, err.Error(), "timeout", "error should mention timeout")
}

func TestAgent_EarlyExitDetected(t *testing.T) {
	ctx := context.Background()

	// Spawn a process that writes to stderr and exits immediately,
	// simulating Claude Code rejecting a nested session.
	startEarlyExit := func(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
		ctx2, cancel := context.WithCancel(ctx)

		cmd := exec.CommandContext(ctx2, os.Args[0], "-test.run=TestHelperProcessEarlyExit", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS_EARLY_EXIT=1")
		cmd.Dir = opts.WorkingDir

		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, err
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, err
		}

		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf

		a := &Agent{
			agentID:        opts.AgentID,
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			cmd:            cmd,
			stdin:          stdin,
			ctx:            ctx2,
			cancel:         cancel,
			stderrBuf:      &stderrBuf,
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

		cleanup := func() {
			a.Stop()
			_ = a.Wait()
		}

		// Attempt the initialize handshake — should detect early exit.
		if _, err := a.sendControlAndWait(ctx2, `{"subtype":"initialize"}`, opts.startupTimeout()); err != nil {
			cleanup()
			return nil, fmt.Errorf("initialize: %w", err)
		}

		return a, nil
	}

	start := time.Now()
	agent, err := startEarlyExit(ctx, Options{
		AgentID:        "early-exit-test",
		Model:          "test",
		WorkingDir:     t.TempDir(),
		StartupTimeout: 5 * time.Second,
	}, func([]byte) {})
	elapsed := time.Since(start)

	assert.Nil(t, agent, "agent should be nil on early exit")
	require.Error(t, err, "expected error from early exit")
	assert.Contains(t, err.Error(), "cannot be launched inside another Claude Code session",
		"error should include the stderr message from the crashed process")
	assert.Less(t, elapsed, 2*time.Second,
		"should detect early exit quickly, not wait for the full 5s timeout")
}
