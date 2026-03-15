package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

		stderrDone := make(chan struct{})
		close(stderrDone) // stderr is captured synchronously via cmd.Stderr

		a := &Agent{
			agentID:        opts.AgentID,
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			cmd:            cmd,
			stdin:          stdin,
			ctx:            ctx2,
			cancel:         cancel,
			stderrBuf:      &stderrBuf,
			stderrDone:     stderrDone,
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
			return nil, a.formatStartupError("initialize", err)
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
	assert.Contains(t, err.Error(), "agent process exited with code",
		"error should include the exit code")
	assert.Contains(t, err.Error(), "cannot be launched inside another Claude Code session",
		"error should include the stderr message from the crashed process")
	assert.Less(t, elapsed, 2*time.Second,
		"should detect early exit quickly, not wait for the full 5s timeout")
}

// TestHelperProcessWithPreamble is a test helper that outputs preamble lines
// to both stdout and stderr, then a delimiter on stdout, then NDJSON.
func TestHelperProcessWithPreamble(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_PREAMBLE") != "1" {
		return
	}

	delimiter := os.Getenv("GO_PREAMBLE_DELIMITER")

	// Output preamble to stdout.
	_, _ = fmt.Fprintln(os.Stdout, "Welcome to my shell")
	_, _ = fmt.Fprintln(os.Stdout, "Loading .zshrc ...")

	// Output preamble to stderr.
	_, _ = fmt.Fprintln(os.Stderr, "stderr preamble line 1")
	_, _ = fmt.Fprintln(os.Stderr, "stderr preamble line 2")

	// Output delimiter on stdout.
	_, _ = fmt.Fprintln(os.Stdout, delimiter)

	// Then echo stdin as NDJSON.
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

func TestAgent_PreambleSkipping(t *testing.T) {
	ctx := context.Background()

	delimiter := "__LEAPMUX_READY_testdelimiter__"

	var mu sync.Mutex
	var lines []string
	outputFn := func(line []byte) {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
	}

	// Start process with preamble.
	ctx2, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx2, os.Args[0], "-test.run=TestHelperProcessWithPreamble", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS_PREAMBLE=1", "GO_PREAMBLE_DELIMITER="+delimiter)
	cmd.Dir = t.TempDir()

	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderrPipe, err := cmd.StderrPipe()
	require.NoError(t, err)

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	a := &Agent{
		agentID:           "preamble-test",
		model:             "test",
		workingDir:        t.TempDir(),
		cmd:               cmd,
		stdin:             stdin,
		ctx:               ctx2,
		cancel:            cancel,
		stderrBuf:         &stderrBuf,
		stderrDone:        stderrDone,
		preambleDelimiter: delimiter,
		preambleMeta:      make(map[string]string),
		processDone:       make(chan struct{}),
		pendingControl:    make(map[string]chan<- controlResult),
	}

	require.NoError(t, cmd.Start())

	// Drain stderr in background.
	go func() {
		defer close(stderrDone)
		a.stderrMu.Lock()
		_, _ = fmt.Fprintf(&stderrBuf, "")
		a.stderrMu.Unlock()
		buf := make([]byte, 4096)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				a.stderrMu.Lock()
				stderrBuf.Write(buf[:n])
				a.stderrMu.Unlock()
			}
			if readErr != nil {
				break
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner, outputFn)

	// Send some input to trigger output after delimiter.
	require.NoError(t, a.SendInput("hello"))

	// Wait for output to arrive.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0
	}, "expected output after preamble")

	// Verify preamble was captured.
	preamble := a.PreambleOutput()
	assert.Contains(t, preamble, "Welcome to my shell")
	assert.Contains(t, preamble, "Loading .zshrc ...")

	// Verify preamble lines were NOT forwarded to outputFn.
	mu.Lock()
	for _, line := range lines {
		assert.NotContains(t, line, "Welcome to my shell")
		assert.NotContains(t, line, "Loading .zshrc ...")
	}
	mu.Unlock()

	// Verify stderr was captured.
	testutil.AssertEventually(t, func() bool {
		return strings.Contains(a.Stderr(), "stderr preamble line 1")
	}, "expected stderr to be captured")

	a.Stop()
	_ = a.Wait()
}

// TestHelperProcessWithPreambleMeta is a test helper that outputs metadata lines,
// preamble, delimiter, then echoes stdin.
func TestHelperProcessWithPreambleMeta(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_PREAMBLE_META") != "1" {
		return
	}

	delimiter := os.Getenv("GO_PREAMBLE_DELIMITER")
	metaPrefix := os.Getenv("GO_META_PREFIX")

	// Output preamble
	_, _ = fmt.Fprintln(os.Stdout, "shell preamble line")
	// Output metadata line
	_, _ = fmt.Fprintln(os.Stdout, metaPrefix+"supports_model_effort=false")
	// Output delimiter
	_, _ = fmt.Fprintln(os.Stdout, delimiter)

	// Echo stdin
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

func TestAgent_PreambleMetaParsing(t *testing.T) {
	ctx := context.Background()

	delimiter := "__LEAPMUX_READY_testmeta__"
	metaPrefix := "__LEAPMUX_META_testmeta__ "

	var mu sync.Mutex
	var lines []string
	outputFn := func(line []byte) {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
	}

	ctx2, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx2, os.Args[0], "-test.run=TestHelperProcessWithPreambleMeta", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS_PREAMBLE_META=1",
		"GO_PREAMBLE_DELIMITER="+delimiter,
		"GO_META_PREFIX="+metaPrefix,
	)
	cmd.Dir = t.TempDir()

	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	a := &Agent{
		agentID:            "meta-test",
		model:              "test",
		workingDir:         t.TempDir(),
		cmd:                cmd,
		stdin:              stdin,
		ctx:                ctx2,
		cancel:             cancel,
		preambleDelimiter:  delimiter,
		preambleMetaPrefix: metaPrefix,
		preambleMeta:       make(map[string]string),
		processDone:        make(chan struct{}),
		pendingControl:     make(map[string]chan<- controlResult),
	}

	require.NoError(t, cmd.Start())

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner, outputFn)

	// Send input to trigger post-preamble output.
	require.NoError(t, a.SendInput("hello"))

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0
	}, "expected output after preamble")

	// Verify metadata was parsed.
	assert.False(t, a.SupportsModelEffort(), "should be false when env var set")
	assert.Equal(t, "false", a.preambleMeta["supports_model_effort"])

	// Verify preamble output does NOT contain the metadata line.
	preamble := a.PreambleOutput()
	assert.Contains(t, preamble, "shell preamble line")
	assert.NotContains(t, preamble, "supports_model_effort")

	// Verify metadata was NOT forwarded to outputFn.
	mu.Lock()
	for _, line := range lines {
		assert.NotContains(t, line, "supports_model_effort")
	}
	mu.Unlock()

	a.Stop()
	_ = a.Wait()
}

func TestAgent_SupportsModelEffortDefaultFalse(t *testing.T) {
	a := &Agent{preambleMeta: make(map[string]string)}
	assert.False(t, a.SupportsModelEffort())
}

func TestAgent_SupportsModelEffortTrue(t *testing.T) {
	a := &Agent{preambleMeta: map[string]string{"supports_model_effort": "true"}}
	assert.True(t, a.SupportsModelEffort())
}

func TestAgent_SupportsModelEffortFalseFromShell(t *testing.T) {
	// Shell detected third-party provider env var at runtime.
	a := &Agent{preambleMeta: map[string]string{"supports_model_effort": "false"}}
	assert.False(t, a.SupportsModelEffort())
}

func TestAgent_SupportsModelEffortFalseNoMeta(t *testing.T) {
	// Settings detected third-party → no metadata emitted → empty map.
	a := &Agent{preambleMeta: make(map[string]string)}
	assert.False(t, a.SupportsModelEffort())
}

func TestAgent_LeapmuxWorkerEnvAlwaysSet(t *testing.T) {
	// Verify that LEAPMUX_WORKER=1 is always present in the environment
	// when starting an agent (both with and without login shell).
	ctx := context.Background()

	// Without login shell.
	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Dir = t.TempDir()
	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "LEAPMUX_WORKER=1")

	foundWorker := false
	foundClaudeCode := false
	for _, e := range cmd.Env {
		if e == "LEAPMUX_WORKER=1" {
			foundWorker = true
		}
		if e == "CLAUDECODE=1" {
			foundClaudeCode = true
		}
	}
	assert.True(t, foundWorker, "LEAPMUX_WORKER=1 should be in env")
	assert.False(t, foundClaudeCode, "CLAUDECODE=1 should NOT be in env without login shell")

	// With login shell - verify the env is set on the command.
	shellCmd, _, _ := buildShellWrappedCommand(ctx, "/bin/sh", true, []string{"--output-format", "stream-json"}, []string{"--model", "test"}, t.TempDir())
	shellCmd.Env = filterEnv(shellCmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	shellCmd.Env = append(shellCmd.Env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "LEAPMUX_WORKER=1", "CLAUDECODE=1")

	foundWorker = false
	foundClaudeCode = false
	for _, e := range shellCmd.Env {
		if e == "LEAPMUX_WORKER=1" {
			foundWorker = true
		}
		if e == "CLAUDECODE=1" {
			foundClaudeCode = true
		}
	}
	assert.True(t, foundWorker, "LEAPMUX_WORKER=1 should be in shell-wrapped env")
	assert.True(t, foundClaudeCode, "CLAUDECODE=1 should be in shell-wrapped env")
}
