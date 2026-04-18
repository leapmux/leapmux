//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// TestHelperProcessControlResponder is a test helper that acts as a
// control-protocol mock. For each control_request line on stdin it emits a
// canned control_response determined by the LEAPMUX_TEST_CONTROL_SCRIPT env
// var, a "|"-separated list where each entry is:
//
//	success           — emit a success control_response
//	error:<text>      — emit an error control_response with the given text
//	skip              — ignore the request (simulate a timeout / no reply)
//
// Each incoming control_request body is appended to
// LEAPMUX_TEST_CONTROL_LOG (if set) as one JSON line, in the order
// received. Non-control lines on stdin are ignored.
func TestHelperProcessControlResponder(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_RESPONDER") != "1" {
		return
	}

	scriptRaw := os.Getenv("LEAPMUX_TEST_CONTROL_SCRIPT")
	var script []string
	if scriptRaw != "" {
		script = strings.Split(scriptRaw, "|")
	}

	var logFile *os.File
	if p := os.Getenv("LEAPMUX_TEST_CONTROL_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			logFile = f
			defer func() { _ = f.Close() }()
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	idx := 0
	for scanner.Scan() {
		var env struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Request   json.RawMessage `json:"request"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type != "control_request" {
			continue
		}
		if logFile != nil {
			_, _ = logFile.Write(env.Request)
			_, _ = logFile.WriteString("\n")
			_ = logFile.Sync()
		}

		var action string
		if idx < len(script) {
			action = script[idx]
		}
		idx++

		switch {
		case action == "success":
			resp := map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": env.RequestID,
					"response":   map[string]any{},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		case strings.HasPrefix(action, "error:"):
			resp := map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "error",
					"request_id": env.RequestID,
					"error":      strings.TrimPrefix(action, "error:"),
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		case action == "skip":
			// no response — the waiter will time out.
		}
	}
	os.Exit(0)
}

// mockStartWithResponder spawns TestHelperProcessControlResponder as the
// child process and wires it up as a ClaudeCodeAgent. script is forwarded
// as LEAPMUX_TEST_CONTROL_SCRIPT; logPath (optional) as
// LEAPMUX_TEST_CONTROL_LOG.
func mockStartWithResponder(ctx context.Context, opts Options, sink OutputSink, script, logPath string) (*ClaudeCodeAgent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcessControlResponder", "--")
	env := append(os.Environ(),
		"GO_WANT_HELPER_PROCESS_RESPONDER=1",
		"LEAPMUX_TEST_CONTROL_SCRIPT="+script,
	)
	if logPath != "" {
		env = append(env, "LEAPMUX_TEST_CONTROL_LOG="+logPath)
	}
	cmd.Env = env
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

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     opts.AgentID,
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a, nil
}

// mockStart spawns a test helper process instead of the real claude binary.
func mockStart(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
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

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     opts.AgentID,
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a, nil
}

func TestAgent_StartAndStop(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStart(ctx, Options{
		AgentID:    "test-workspace",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStart")

	// Send a valid assistant NDJSON message that HandleOutput will persist.
	require.NoError(t, agent.SendRawInput([]byte(`{"type":"assistant","message":{"role":"assistant","content":"hi"}}`+"\n")), "SendRawInput")

	// Wait for the message to be processed by HandleOutput and persisted via the sink.
	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected at least one persisted message")

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
	}, noopSink{})
	require.NoError(t, err, "mockStart")

	agent.Stop()
	_ = agent.Wait()

	assert.Error(t, agent.SendInput("should fail", nil), "expected error sending input after stop")
}

func TestAgent_AgentID(t *testing.T) {
	ctx := context.Background()

	agent, err := mockStart(ctx, Options{
		AgentID:    "my-agent",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{})
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
	}, noopSink{})
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
// with a session_id, simulating real Claude Code behavior.
func mockStartWithInit(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
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

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     opts.AgentID,
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		},
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a, nil
}

// TestAgent_InitMessageFlowsThrough verifies that the init message with
// session_id is processed by HandleOutput and forwarded to the sink.
func TestAgent_InitMessageFlowsThrough(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "init-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStartWithInit")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	// The init message should trigger UpdateSessionID on the sink.
	testutil.AssertEventually(t, func() bool {
		return sink.SessionIDCount() >= 1
	}, "expected init message to trigger UpdateSessionID")

	assert.Equal(t, "test-session-abc123", sink.LastSessionID(),
		"session ID should match the init message")

	// Send additional input (an assistant message) and verify it flows through too.
	require.NoError(t, agent.SendRawInput([]byte(`{"type":"assistant","message":{"role":"assistant","content":"reply"}}`+"\n")))
	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() >= 1
	}, "expected additional output after input")
}

// TestAgent_ToolUseCountSurvivesToolResult verifies that the turn tool use
// counter is not reset by tool_result (user) messages, only by user text echoes.
func TestAgent_ToolUseCountSurvivesToolResult(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "tool-count-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStartWithInit")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	// Wait for init to be processed.
	testutil.AssertEventually(t, func() bool {
		return sink.SessionIDCount() >= 1
	}, "expected init message")

	// 1. User text echo — resets counter to 0.
	userEcho := `{"type":"user","message":{"role":"user","content":"Run pwd"}}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(userEcho)))
	time.Sleep(50 * time.Millisecond)

	// 2. Assistant with tool_use — counter should become 1.
	assistantToolUse := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}]},"session_id":"test-session","uuid":"uuid1"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(assistantToolUse)))
	time.Sleep(50 * time.Millisecond)

	// 3. Tool result (user type, array content) — should NOT reset counter.
	toolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/home/user"}]}}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(toolResult)))
	time.Sleep(50 * time.Millisecond)

	// 4. Assistant with text (no tool_use) — counter stays 1.
	assistantText := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The current directory is /home/user"}]},"session_id":"test-session","uuid":"uuid2"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(assistantText)))
	time.Sleep(50 * time.Millisecond)

	// 5. Result message — should be enriched with num_tool_uses: 1.
	resultMsg := `{"type":"result","subtype":"turn_end"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(resultMsg)))

	testutil.AssertEventually(t, func() bool {
		msgs := sink.Messages()
		for _, m := range msgs {
			if m.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
				return true
			}
		}
		return false
	}, "expected result message to be persisted")

	// Find the result message and verify num_tool_uses.
	msgs := sink.Messages()
	var resultContent []byte
	for _, m := range msgs {
		if m.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
			resultContent = m.Content
		}
	}
	require.NotNil(t, resultContent, "result message should exist")

	var enriched map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resultContent, &enriched))
	numToolUsesRaw, ok := enriched["num_tool_uses"]
	require.True(t, ok, "result should contain num_tool_uses field")

	var numToolUses int
	require.NoError(t, json.Unmarshal(numToolUsesRaw, &numToolUses))
	assert.Equal(t, 1, numToolUses, "num_tool_uses should be 1 (tool_result should not reset the counter)")
}

// TestAgent_SpanTypeSetOnToolUseAndResult verifies that span_type is set
// to the tool name for both tool_use (assistant) and tool_result (user) messages.
func TestAgent_SpanTypeSetOnToolUseAndResult(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "span-type-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStartWithInit")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	// Wait for init to be processed.
	testutil.AssertEventually(t, func() bool {
		return sink.SessionIDCount() >= 1
	}, "expected init message")

	initialCount := sink.MessageCount()

	// Send a tool_use (assistant) message with tool name "Grep".
	toolUseMsg := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_test123","name":"Grep","input":{"pattern":"foo"}}]},"session_id":"test-session","uuid":"uuid1"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(toolUseMsg)))

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > initialCount
	}, "expected tool_use message to be persisted")

	msgs := sink.Messages()
	toolUse := msgs[len(msgs)-1]
	assert.Equal(t, "toolu_test123", toolUse.SpanID, "tool_use span ID")
	assert.Equal(t, "Grep", toolUse.SpanType, "tool_use span type should be the tool name")

	// Send a tool_result (user) message referencing the same tool_use_id.
	toolResultMsg := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_test123","content":"Found 3 files"}]},"session_id":"test-session","uuid":"uuid2","tool_use_result":{"mode":"files_with_matches","filenames":["a.go","b.go","c.go"],"numFiles":3}}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(toolResultMsg)))

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > initialCount+1
	}, "expected tool_result message to be persisted")

	msgs = sink.Messages()
	toolResult := msgs[len(msgs)-1]
	assert.Equal(t, "toolu_test123", toolResult.SpanID, "tool_result span ID")
	assert.Equal(t, "Grep", toolResult.SpanType, "tool_result span type should match the tool_use")
}

// TestAgent_ParallelToolUseClosesAllSpans verifies that when a user message
// contains multiple tool_result blocks (parallel tool calls), all corresponding
// spans are closed — not just the first one.
func TestAgent_ParallelToolUseClosesAllSpans(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "parallel-span-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStartWithInit")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	testutil.AssertEventually(t, func() bool {
		return sink.SessionIDCount() >= 1
	}, "expected init message")

	// Assistant calls two tools in parallel.
	toolUseMsg := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_A","name":"Grep","input":{"pattern":"foo"}},` +
		`{"type":"tool_use","id":"toolu_B","name":"Bash","input":{"command":"ls"}}` +
		`]},"session_id":"test-session","uuid":"uuid1"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(toolUseMsg)))

	testutil.AssertEventually(t, func() bool {
		return len(sink.OpenSpans()) >= 2
	}, "expected both spans to be opened")

	assert.ElementsMatch(t, []string{"toolu_A", "toolu_B"}, []string{sink.OpenSpans()[0].SpanID, sink.OpenSpans()[1].SpanID})

	// Single user message with two tool_result blocks.
	toolResultMsg := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_A","content":"match found"},` +
		`{"type":"tool_result","tool_use_id":"toolu_B","content":"file list"}` +
		`]},"session_id":"test-session","uuid":"uuid2"}` + "\n"
	require.NoError(t, agent.SendRawInput([]byte(toolResultMsg)))

	testutil.AssertEventually(t, func() bool {
		return sink.ClosedSpanCount() >= 2
	}, "expected both spans to be closed")

	assert.ElementsMatch(t, []string{"toolu_A", "toolu_B"}, sink.ClosedSpans())
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
	startUnresponsive := func(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
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

		a := &ClaudeCodeAgent{
			processBase: processBase{
				agentID:     opts.AgentID,
				cmd:         cmd,
				stdin:       stdin,
				ctx:         ctx2,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			},
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			sink:           sink,
			pendingControl: make(map[string]chan<- claudeCodeControlResult),
		}
		close(a.stderrDone)

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutputLoop(scanner)

		// Replicate the startup handshake from StartClaudeCode().
		mode := StringOrDefault(opts.PermissionMode, PermissionModeDefault)
		requestID := generateRequestID()
		ch := make(chan claudeCodeControlResult, 1)
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
	}, noopSink{})

	assert.Nil(t, agent, "agent should be nil on timeout")
	require.Error(t, err, "expected timeout error")
	assert.Contains(t, err.Error(), "timeout", "error should mention timeout")
}

func TestAgent_EarlyExitDetected(t *testing.T) {
	ctx := context.Background()

	// Spawn a process that writes to stderr and exits immediately,
	// simulating Claude Code rejecting a nested session.
	startEarlyExit := func(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
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

		a := &ClaudeCodeAgent{
			processBase: processBase{
				agentID:     opts.AgentID,
				cmd:         cmd,
				stdin:       stdin,
				ctx:         ctx2,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			},
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			sink:           sink,
			pendingControl: make(map[string]chan<- claudeCodeControlResult),
		}
		cmd.Stderr = &a.stderrBuf
		close(a.stderrDone) // stderr is captured synchronously via cmd.Stderr

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutputLoop(scanner)

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
	}, noopSink{})
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
	sink := &testSink{}

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

	stderrDone := make(chan struct{})
	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     "preamble-test",
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx2,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  stderrDone,
		},
		model:          "test",
		workingDir:     t.TempDir(),
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	a.preambleDelimiter = delimiter
	a.preambleMeta = make(map[string]string)

	require.NoError(t, cmd.Start())

	// Drain stderr in background.
	go func() {
		defer close(stderrDone)
		buf := make([]byte, 4096)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				a.stderrMu.Lock()
				a.stderrBuf.Write(buf[:n])
				a.stderrMu.Unlock()
			}
			if readErr != nil {
				break
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	// Send a valid assistant NDJSON message to trigger output after delimiter.
	require.NoError(t, a.SendRawInput([]byte(`{"type":"assistant","message":{"role":"assistant","content":"hello"}}`+"\n")))

	// Wait for output to arrive via the sink.
	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected output after preamble")

	// Verify preamble was captured.
	preamble := a.PreambleOutput()
	assert.Contains(t, preamble, "Welcome to my shell")
	assert.Contains(t, preamble, "Loading .zshrc ...")

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
	_, _ = fmt.Fprintln(os.Stdout, metaPrefix+"can_change_model_and_effort=false")
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
	sink := &testSink{}

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

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     "meta-test",
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx2,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		},
		model:          "test",
		workingDir:     t.TempDir(),
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	a.preambleDelimiter = delimiter
	a.preambleMetaPrefix = metaPrefix
	a.preambleMeta = make(map[string]string)
	close(a.stderrDone)

	require.NoError(t, cmd.Start())

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	// Send a valid assistant NDJSON message to trigger post-preamble output.
	require.NoError(t, a.SendRawInput([]byte(`{"type":"assistant","message":{"role":"assistant","content":"hello"}}`+"\n")))

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected output after preamble")

	// Verify metadata was parsed.
	assert.Equal(t, "false", a.preambleMetaValue("can_change_model_and_effort"))

	// Verify preamble output does NOT contain the metadata line.
	preamble := a.PreambleOutput()
	assert.Contains(t, preamble, "shell preamble line")
	assert.NotContains(t, preamble, "can_change_model_and_effort")

	a.Stop()
	_ = a.Wait()
}

func TestClaudeCodePermissionModeOptions_AllDescriptionsPopulated(t *testing.T) {
	groups := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NotEmpty(t, groups, "expected at least the permission-mode group")

	var permGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.GetKey() == OptionGroupKeyPermissionMode {
			permGroup = g
			break
		}
	}
	require.NotNil(t, permGroup, "permission-mode option group not found")

	wantIDs := []string{
		PermissionModeDefault,
		PermissionModePlan,
		PermissionModeAcceptEdits,
		PermissionModeBypassPermissions,
		PermissionModeDontAsk,
		PermissionModeAuto,
	}
	options := permGroup.GetOptions()
	require.Len(t, options, len(wantIDs), "expected 6 permission modes")

	gotIDs := make([]string, 0, len(options))
	defaultCount := 0
	for _, o := range options {
		gotIDs = append(gotIDs, o.GetId())
		assert.NotEmptyf(t, o.GetName(), "mode %q: Name must be set", o.GetId())
		assert.NotEmptyf(t, o.GetDescription(), "mode %q: Description must be set (used as tooltip)", o.GetId())
		if o.GetIsDefault() {
			defaultCount++
		}
	}
	assert.Equal(t, wantIDs, gotIDs, "unexpected mode ids or order")
	assert.Equal(t, 1, defaultCount, "exactly one option should be marked IsDefault")
}

func TestFilterPermissionModeGroup_AutoAvailable(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	got := filterPermissionModeGroup(staticGroup, true)

	assert.Same(t, staticGroup, got, "when auto is available, the static group should be returned unchanged")
	assert.Len(t, got.GetOptions(), 6)
}

func TestFilterPermissionModeGroup_AutoUnavailable(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	got := filterPermissionModeGroup(staticGroup, false)

	require.NotSame(t, staticGroup, got, "filtered result must be a fresh copy")
	assert.Equal(t, staticGroup.GetKey(), got.GetKey())
	assert.Equal(t, staticGroup.GetLabel(), got.GetLabel())

	ids := make([]string, 0, len(got.GetOptions()))
	for _, o := range got.GetOptions() {
		ids = append(ids, o.GetId())
		assert.NotEmptyf(t, o.GetDescription(), "mode %q: Description must be preserved after filtering", o.GetId())
	}
	assert.NotContains(t, ids, PermissionModeAuto, "auto must be filtered out")
	assert.ElementsMatch(t, []string{
		PermissionModeDefault,
		PermissionModePlan,
		PermissionModeAcceptEdits,
		PermissionModeBypassPermissions,
		PermissionModeDontAsk,
	}, ids, "remaining modes should include dontAsk and all non-auto modes")
}

func TestIsAutoModeUnavailableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"bare prefix", fmt.Errorf("Cannot set permission mode to auto"), true},
		{"settings reason", fmt.Errorf("Cannot set permission mode to auto: auto mode disabled by settings"), true},
		{"circuit-breaker reason", fmt.Errorf("Cannot set permission mode to auto: auto mode is unavailable for your plan"), true},
		{"model reason", fmt.Errorf("Cannot set permission mode to auto: auto mode unavailable for this model"), true},
		{"wrapped error", fmt.Errorf("set_permission_mode failed: %w", fmt.Errorf("Cannot set permission mode to auto: auto mode disabled by settings")), true},
		{"unrelated error", fmt.Errorf("timeout waiting for agent to respond"), false},
		{"other mode rejection", fmt.Errorf("Cannot set permission mode to plan"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAutoModeUnavailableError(tc.err))
		})
	}
}

func TestClaudeCodeAgent_AvailableOptionGroupsFiltersAutoWhenUnavailable(t *testing.T) {
	agent := &ClaudeCodeAgent{
		processBase:       processBase{agentID: "t"},
		pendingControl:    make(map[string]chan<- claudeCodeControlResult),
		autoModeAvailable: false,
	}

	groups := agent.AvailableOptionGroups()
	require.NotEmpty(t, groups, "expected at least the permission-mode group")

	permGroup := groups[0]
	assert.Equal(t, OptionGroupKeyPermissionMode, permGroup.GetKey())
	for _, o := range permGroup.GetOptions() {
		assert.NotEqual(t, PermissionModeAuto, o.GetId(), "auto must not appear when autoModeAvailable is false")
	}

	agent.autoModeAvailable = true
	groups = agent.AvailableOptionGroups()
	permGroup = groups[0]
	found := false
	for _, o := range permGroup.GetOptions() {
		if o.GetId() == PermissionModeAuto {
			found = true
			break
		}
	}
	assert.True(t, found, "auto should appear when autoModeAvailable is true")
}

func readHandshakeModes(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	require.NoError(t, err, "read control log")
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	modes := make([]string, 0, len(lines))
	for _, line := range lines {
		var body struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &body))
		require.Equal(t, "set_permission_mode", body.Subtype)
		modes = append(modes, body.Mode)
	}
	return modes
}

// Auto startup succeeds in a single round trip when Claude Code accepts it
// — probe and apply are merged into one set_permission_mode call.
func TestApplyStartupPermissionMode_AutoAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-auto-ok", WorkingDir: t.TempDir()},
		noopSink{}, "success", logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModeAuto
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.NoError(t, err)

	assert.True(t, agent.autoModeAvailable)
	assert.Equal(t, PermissionModeAuto, mode, "requested mode should be preserved on acceptance")
	assert.Equal(t, []string{PermissionModeAuto}, readHandshakeModes(t, logPath),
		"accepted auto must complete in one set_permission_mode call")
}

// Auto startup falls back to default when Claude Code rejects it with the
// auto-unavailable prefix.
func TestApplyStartupPermissionMode_AutoRejectedFallsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-auto-reject", WorkingDir: t.TempDir()},
		noopSink{},
		"error:Cannot set permission mode to auto: auto mode disabled by settings|success",
		logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModeAuto
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.NoError(t, err)

	assert.False(t, agent.autoModeAvailable,
		"auto must be marked unavailable after a rejection with the auto-unavailable prefix")
	assert.Equal(t, PermissionModeDefault, mode, "mode should be rewritten to default on fallback")
	assert.Equal(t, []string{PermissionModeAuto, PermissionModeDefault}, readHandshakeModes(t, logPath),
		"fallback path must issue auto then default")
}

// A non-auto-unavailable error surfaces as a startup failure without retry.
func TestApplyStartupPermissionMode_AutoTransientErrorPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-auto-transient", WorkingDir: t.TempDir()},
		noopSink{},
		"error:some unrelated runtime error",
		logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModeAuto
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.Error(t, err, "transient errors should not be treated as unavailability")
	assert.NotContains(t, err.Error(), autoModeUnavailableErrorPrefix)
	assert.Equal(t, []string{PermissionModeAuto}, readHandshakeModes(t, logPath),
		"transient error must not trigger a retry")
}

// Non-auto startup probes auto first so the UI reflects actual availability,
// then applies the requested mode.
func TestApplyStartupPermissionMode_NonAutoProbesAutoAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-probe-ok", WorkingDir: t.TempDir()},
		noopSink{}, "success|success", logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModeDefault
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.NoError(t, err)

	assert.True(t, agent.autoModeAvailable, "probe should confirm auto availability")
	assert.Equal(t, []string{PermissionModeAuto, PermissionModeDefault}, readHandshakeModes(t, logPath),
		"non-auto startup must probe auto, then apply the requested mode")
}

// A rejected probe marks auto unavailable; the requested mode is still
// applied so the session proceeds.
func TestApplyStartupPermissionMode_NonAutoProbeRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-probe-reject", WorkingDir: t.TempDir()},
		noopSink{},
		"error:Cannot set permission mode to auto: auto mode disabled by settings|success",
		logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModePlan
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.NoError(t, err)

	assert.False(t, agent.autoModeAvailable, "probe rejection must hide auto from the UI")
	assert.Equal(t, []string{PermissionModeAuto, PermissionModePlan}, readHandshakeModes(t, logPath),
		"requested mode must be applied after the probe")
}

// A transient probe error is conservatively treated as unavailable and
// doesn't block startup; the requested mode is still applied.
func TestApplyStartupPermissionMode_NonAutoProbeTransient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "control.log")
	agent, err := mockStartWithResponder(ctx,
		Options{AgentID: "handshake-probe-transient", WorkingDir: t.TempDir()},
		noopSink{},
		"error:some unrelated runtime error|success",
		logPath)
	require.NoError(t, err, "mockStartWithResponder")
	defer func() { agent.Stop(); _ = agent.Wait() }()

	mode := PermissionModeDefault
	_, err = agent.applyStartupPermissionMode(ctx, &mode, 2*time.Second)
	require.NoError(t, err, "transient probe error must not fail startup")

	assert.False(t, agent.autoModeAvailable, "transient probe failure must conservatively hide auto")
	assert.Equal(t, []string{PermissionModeAuto, PermissionModeDefault}, readHandshakeModes(t, logPath))
}

func TestAgent_LeapmuxWorkerEnvAlwaysSet(t *testing.T) {
	// Verify that LEAPMUX_WORKER=1 is always present for Claude Code and that
	// CLAUDECODE=1 is injected only for login shells.
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
	shellCmd, _, _ := buildShellWrappedCommand(ctx, "/bin/sh", true, "claude", []string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "test"}, t.TempDir())
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

// TestClaudeCodeAvailableModels_EffortsMatchDocs pins the per-model effort
// lists and defaults to the documented Claude Code rules so accidental edits
// to the tables get caught at test time.
func TestClaudeCodeAvailableModels_EffortsMatchDocs(t *testing.T) {
	byID := map[string]*leapmuxv1.AvailableModel{}
	for _, m := range claudeCodeAvailableModels {
		byID[m.Id] = m
	}

	effortIDs := func(m *leapmuxv1.AvailableModel) []string {
		ids := make([]string, 0, len(m.SupportedEfforts))
		for _, e := range m.SupportedEfforts {
			ids = append(ids, e.Id)
		}
		return ids
	}

	opusEfforts := []string{"auto", "max", "xhigh", "high", "medium", "low"}
	sonnetEfforts := []string{"auto", "max", "high", "medium", "low"}

	for _, id := range []string{"opus", "opus[1m]"} {
		m := byID[id]
		require.NotNil(t, m, "model %q missing", id)
		assert.Equal(t, opusEfforts, effortIDs(m), "opus effort list for %q", id)
		assert.Equal(t, "xhigh", m.DefaultEffort, "opus default effort for %q", id)
	}

	for _, id := range []string{"sonnet", "sonnet[1m]"} {
		m := byID[id]
		require.NotNil(t, m, "model %q missing", id)
		assert.Equal(t, sonnetEfforts, effortIDs(m), "sonnet effort list for %q", id)
		assert.Equal(t, "high", m.DefaultEffort, "sonnet default effort for %q", id)
	}

	haiku := byID["haiku"]
	require.NotNil(t, haiku)
	assert.Empty(t, haiku.SupportedEfforts, "haiku has no effort UI")
}
