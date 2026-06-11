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
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
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
	env := []string{
		"GO_WANT_HELPER_PROCESS_RESPONDER=1",
		"LEAPMUX_TEST_CONTROL_SCRIPT=" + script,
	}
	if logPath != "" {
		env = append(env, "LEAPMUX_TEST_CONTROL_LOG="+logPath)
	}
	return spawnMockClaudeAgent(ctx, "TestHelperProcessControlResponder", env, opts, sink)
}

// mockStart spawns a test helper process instead of the real claude binary.
func mockStart(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
	return spawnMockClaudeAgent(ctx, "TestHelperProcess", []string{"GO_WANT_HELPER_PROCESS=1"}, opts, sink)
}

func TestAgent_StartAndStop(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStart(ctx, Options{
		AgentID:    "test-workspace",
		Options:    map[string]string{OptionIDModel: "test"},
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
		Options:    map[string]string{OptionIDModel: "test"},
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
		Options:    map[string]string{OptionIDModel: "test"},
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
		Options:    map[string]string{OptionIDModel: "test"},
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
	return spawnMockClaudeAgent(ctx, "TestHelperProcessWithInit",
		[]string{"GO_WANT_HELPER_PROCESS_WITH_INIT=1"}, opts, sink)
}

// TestAgent_InitMessageFlowsThrough verifies that the init message with
// session_id is processed by HandleOutput and forwarded to the sink.
func TestAgent_InitMessageFlowsThrough(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStartWithInit(ctx, Options{
		AgentID:    "init-test",
		Options:    map[string]string{OptionIDModel: "test"},
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
		Options:    map[string]string{OptionIDModel: "test"},
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
			if m.TurnEnd {
				return true
			}
		}
		return false
	}, "expected result message to be persisted")

	// Find the result message (recorded by PersistTurnEnd) and verify num_tool_uses.
	msgs := sink.Messages()
	var resultContent []byte
	for _, m := range msgs {
		if m.TurnEnd {
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
		Options:    map[string]string{OptionIDModel: "test"},
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
		Options:    map[string]string{OptionIDModel: "test"},
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
			model:          opts.Model(),
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
		mode := StringOrDefault(opts.PermissionMode(), PermissionModeDefault)
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
		Options:        map[string]string{OptionIDModel: "test"},
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
			model:          opts.Model(),
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
		Options:        map[string]string{OptionIDModel: "test"},
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
		if g.GetId() == OptionIDPermissionMode {
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
		if o.GetId() == permGroup.GetDefaultValue() {
			defaultCount++
		}
	}
	assert.Equal(t, wantIDs, gotIDs, "unexpected mode ids or order")
	assert.Equal(t, 1, defaultCount, "exactly one option should match the group's DefaultValue")
}

func TestFilterPermissionModeGroup_AutoAvailable(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	got := livePermissionModeGroup(staticGroup, PermissionModePlan, true)

	// When auto is available, the options list is shared (unfiltered) and the
	// current value is overlaid onto a fresh live group.
	assert.Len(t, got.GetOptions(), 6)
	assert.Equal(t, PermissionModePlan, got.GetCurrentValue())
}

func TestFilterPermissionModeGroup_AutoUnavailable(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	got := livePermissionModeGroup(staticGroup, PermissionModeDefault, false)

	require.NotSame(t, staticGroup, got, "filtered result must be a fresh copy")
	assert.Equal(t, staticGroup.GetId(), got.GetId())
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

// TestLivePermissionModeGroup_KeepsCurrentAutoWhenUnavailable guards the off-spec-current
// backstop: when autoModeAvailable is stale-false (a transient startup probe failure) but the
// session was switched live to "auto", the current value MUST remain a selectable option.
// Otherwise CurrentValue="auto" has no matching radio and the frontend clamps the displayed
// selection to the default, silently showing the wrong mode while the agent runs in auto.
func TestLivePermissionModeGroup_KeepsCurrentAutoWhenUnavailable(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	got := livePermissionModeGroup(staticGroup, PermissionModeAuto, false)

	assert.Equal(t, PermissionModeAuto, got.GetCurrentValue())
	ids := make([]string, 0, len(got.GetOptions()))
	for _, o := range got.GetOptions() {
		ids = append(ids, o.GetId())
	}
	assert.Contains(t, ids, PermissionModeAuto,
		"the current value must stay selectable even when autoModeAvailable is false")

	// The filter still hides auto when it is NOT the current value (the ordinary unavailable case).
	hidden := livePermissionModeGroup(staticGroup, PermissionModePlan, false)
	hiddenIDs := make([]string, 0, len(hidden.GetOptions()))
	for _, o := range hidden.GetOptions() {
		hiddenIDs = append(hiddenIDs, o.GetId())
	}
	assert.NotContains(t, hiddenIDs, PermissionModeAuto,
		"auto is still hidden when it is unavailable and not the current value")
}

func TestLivePermissionModeGroup_EmptyCurrentFallsBackToDefault(t *testing.T) {
	staticGroup := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0]

	// An empty current must resolve to the template's default rather than rendering a
	// blank selection (delegated to liveGroup's empty-current fallback).
	got := livePermissionModeGroup(staticGroup, "", true)
	assert.Equal(t, staticGroup.GetDefaultValue(), got.GetCurrentValue())
	assert.Equal(t, PermissionModeDefault, got.GetCurrentValue())
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

	groups := agent.OptionGroups()
	require.NotEmpty(t, groups, "expected at least the permission-mode group")

	permGroup := optionids.GroupByID(groups, OptionIDPermissionMode)
	require.NotNil(t, permGroup)
	for _, o := range permGroup.GetOptions() {
		assert.NotEqual(t, PermissionModeAuto, o.GetId(), "auto must not appear when autoModeAvailable is false")
	}

	agent.autoModeAvailable = true
	groups = agent.OptionGroups()
	permGroup = optionids.GroupByID(groups, OptionIDPermissionMode)
	require.NotNil(t, permGroup)
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

func TestApplyStartupPermissionMode(t *testing.T) {
	cases := []struct {
		name          string
		agentID       string
		script        string
		requested     string
		wantAuto      bool
		wantMode      string
		wantModes     []string
		wantErrSubstr string // non-empty means the call must fail; checks err contains it
	}{
		{
			name:      "AutoAccepted",
			agentID:   "handshake-auto-ok",
			script:    "success",
			requested: PermissionModeAuto,
			wantAuto:  true,
			wantMode:  PermissionModeAuto,
			wantModes: []string{PermissionModeAuto},
		},
		{
			name:      "AutoRejectedFallsBack",
			agentID:   "handshake-auto-reject",
			script:    "error:Cannot set permission mode to auto: auto mode disabled by settings|success",
			requested: PermissionModeAuto,
			wantAuto:  false,
			wantMode:  PermissionModeDefault,
			wantModes: []string{PermissionModeAuto, PermissionModeDefault},
		},
		{
			name:          "AutoTransientErrorPropagates",
			agentID:       "handshake-auto-transient",
			script:        "error:some unrelated runtime error",
			requested:     PermissionModeAuto,
			wantAuto:      false,
			wantModes:     []string{PermissionModeAuto},
			wantErrSubstr: "some unrelated runtime error",
		},
		{
			name:      "NonAutoProbesAutoAvailable",
			agentID:   "handshake-probe-ok",
			script:    "success|success",
			requested: PermissionModeDefault,
			wantAuto:  true,
			wantMode:  PermissionModeDefault,
			wantModes: []string{PermissionModeAuto, PermissionModeDefault},
		},
		{
			name:      "NonAutoProbeRejected",
			agentID:   "handshake-probe-reject",
			script:    "error:Cannot set permission mode to auto: auto mode disabled by settings|success",
			requested: PermissionModePlan,
			wantAuto:  false,
			wantMode:  PermissionModePlan,
			wantModes: []string{PermissionModeAuto, PermissionModePlan},
		},
		{
			name:      "NonAutoProbeTransient",
			agentID:   "handshake-probe-transient",
			script:    "error:some unrelated runtime error|success",
			requested: PermissionModeDefault,
			wantAuto:  false,
			wantMode:  PermissionModeDefault,
			wantModes: []string{PermissionModeAuto, PermissionModeDefault},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			logPath := filepath.Join(t.TempDir(), "control.log")
			agent, err := mockStartWithResponder(ctx,
				Options{AgentID: tc.agentID, WorkingDir: t.TempDir()},
				noopSink{}, tc.script, logPath)
			require.NoError(t, err, "mockStartWithResponder")
			defer func() { agent.Stop(); _ = agent.Wait() }()

			resp, err := agent.applyStartupPermissionMode(ctx, tc.requested, 2*time.Second)
			if tc.wantErrSubstr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.wantMode, resp.Mode)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSubstr)
				assert.NotContains(t, err.Error(), autoModeUnavailableErrorPrefix)
			}

			assert.Equal(t, tc.wantAuto, agent.autoModeAvailable)
			assert.Equal(t, tc.wantModes, readHandshakeModes(t, logPath))
		})
	}
}

func TestAgent_LeapmuxWorkerEnvAlwaysSet(t *testing.T) {
	// Verify that LEAPMUX_WORKER=1 is always present for Claude Code and that
	// CLAUDECODE=1 is injected only for login shells.
	ctx := context.Background()

	// Without login shell.
	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Dir = t.TempDir()
	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
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
	shellCmd, _, _ := wrapShellCmd(ctx, testutil.TestShell(), true, "claude", []string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "test"}, false, t.TempDir())
	shellCmd.Env = envutil.FilterEnv(shellCmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
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

func TestClaudeCodeAvailableModels_EffortsMatchDocs(t *testing.T) {
	byID := claudeModelsByID(claudeCodeAvailableModels)

	opusEfforts := []string{"auto", "ultracode", "max", "xhigh", "high", "medium", "low"}
	sonnetEfforts := []string{"auto", "max", "high", "medium", "low"}

	// Fable and Opus share the full xhigh+ultracode effort menu.
	for _, id := range []string{"fable[1m]", "opus", "opus[1m]"} {
		m := byID[id]
		require.NotNil(t, m, "model %q missing", id)
		assert.Equal(t, opusEfforts, claudeEffortIDs(m), "xhigh effort list for %q", id)
		assert.Equal(t, "xhigh", m.DefaultEffort, "xhigh default effort for %q", id)
	}

	for _, id := range []string{"sonnet", "sonnet[1m]"} {
		m := byID[id]
		require.NotNil(t, m, "model %q missing", id)
		assert.Equal(t, sonnetEfforts, claudeEffortIDs(m), "sonnet effort list for %q", id)
		assert.Equal(t, "high", m.DefaultEffort, "sonnet default effort for %q", id)
	}

	haiku := byID["haiku"]
	require.NotNil(t, haiku)
	assert.Empty(t, haiku.SupportedEfforts, "haiku has no effort UI")
	assert.Equal(t, "", haiku.DefaultEffort, "a no-effort model carries no default effort (matches the dynamic conversion)")
}

// TestClaudeEffortCatalog_UltracodeFollowsXHigh guards the design decision that
// "ultracode" is offered exactly by the xhigh-capable effort menu and not the
// max-only one, so switching to a non-xhigh model (Sonnet) downgrades ultracode
// cleanly. convertClaudeModels keys ultracode off the same xhigh signal, so this
// keeps the static and dynamic catalogs in agreement.
func TestClaudeEffortCatalog_UltracodeFollowsXHigh(t *testing.T) {
	// Use the production effortListContains so this guard exercises the same
	// lookup supportsUltracode relies on, rather than a parallel copy.
	assert.True(t, effortListContains(claudeEffortXHighMax, EffortUltracode), "xhigh catalog must offer ultracode")
	assert.False(t, effortListContains(claudeEffortMax, EffortUltracode), "max-only catalog must not offer ultracode")
}

// TestClaudeDefaultEffort_FallsBackToStrongest verifies that a model which
// supports effort but offers neither xhigh nor high (e.g. a max-only model) still
// gets a concrete default effort -- the strongest recognized level -- rather than
// an empty default while its selector is shown. The xhigh/high product preference
// is preserved when those levels are present.
func TestClaudeDefaultEffort_FallsBackToStrongest(t *testing.T) {
	defaultEffort := func(supportsEffort bool, levels ...string) string {
		return claudeDefaultEffort(normalizedEffortLevelSet(claudeCodeModelInfo{
			SupportsEffort: supportsEffort, SupportedEffortLevels: levels,
		}))
	}
	// Product preference: xhigh wins over max even though max ranks higher.
	assert.Equal(t, "xhigh", defaultEffort(true, "low", "medium", "high", "xhigh", "max"))
	assert.Equal(t, "high", defaultEffort(true, "low", "medium", "high", "max"))
	// Mixed-case CLI levels still resolve (S3 case-normalization).
	assert.Equal(t, "xhigh", defaultEffort(true, "Low", "Medium", "High", "XHigh", "Max"))
	// Neither xhigh nor high: fall back to the strongest recognized level.
	assert.Equal(t, "max", defaultEffort(true, "low", "medium", "max"))
	assert.Equal(t, "medium", defaultEffort(true, "low", "medium"))
	// Only unrecognized levels -> "" (selector hidden, matches claudeSupportedEfforts).
	assert.Equal(t, "", defaultEffort(true, "ludicrous"))
	// No effort support -> "".
	assert.Equal(t, "", defaultEffort(false))

	// End-to-end: a max-only model surfaces with a non-empty selector AND a
	// non-empty default effort (no "selector shown but empty default" state).
	m := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "maxer", DisplayName: "Maxer", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "max"}},
	}, nil))["maxer"]
	require.NotNil(t, m)
	assert.Equal(t, []string{"auto", "max", "medium", "low"}, claudeEffortIDs(m))
	assert.Equal(t, "max", m.DefaultEffort)
}

// TestLaunchOmitsEffort locks the shared predicate that decides when no --effort is
// sent at launch (and so nothing needs reconciling at startup): the account-default
// sentinel and EffortAuto/"" always omit, a model the catalog KNOWS has no effort
// support (Haiku) omits, and unknown models pass their effort through.
func TestLaunchOmitsEffort(t *testing.T) {
	static := newEffortResolver(claudeCodeAvailableModels)
	assert.True(t, static.launchOmitsEffort("default", "high"), "sentinel always omits --effort")
	// S4: an empty model sends no --model so the CLI picks the account default; it
	// must omit --effort too, just like the sentinel.
	assert.True(t, static.launchOmitsEffort("", "high"), "empty model omits --effort")
	assert.True(t, static.launchOmitsEffort("haiku", "high"), "haiku has no efforts in the catalog -> omit")
	assert.True(t, static.launchOmitsEffort("opus", ""), "empty effort omits --effort")
	assert.True(t, static.launchOmitsEffort("opus", EffortAuto), "auto omits --effort")
	assert.False(t, static.launchOmitsEffort("opus", "xhigh"), "a concrete effort on a normal model is sent")
	assert.False(t, static.launchOmitsEffort("sonnet", EffortUltracode))
	// S4: an unknown (not-in-catalog) model is trusted, so its effort is sent.
	assert.False(t, static.launchOmitsEffort("claude-future-preview", "xhigh"), "unknown model passes effort through")
	// S4: an effort-less model discovered dynamically (no "haiku" literal) is handled
	// by the catalog, so it omits --effort just like Haiku.
	dynamic := newEffortResolver(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "quickie", DisplayName: "Quickie", SupportsEffort: false},
	}, nil))
	assert.True(t, dynamic.launchOmitsEffort("quickie", "high"), "catalog-known effort-less model omits --effort")
}

// TestEffortResolver_NilGuard verifies the catalog-walking capability checks
// tolerate nil entries (matching FindAvailableModel/withDefaultModelMarked), so a
// nil-bearing catalog can't panic the effort/ultracode lookups.
func TestEffortResolver_NilGuard(t *testing.T) {
	r := newEffortResolver([]*ModelInfo{nil, {Id: "opus", SupportedEfforts: claudeEffortXHighMax}, nil})
	assert.True(t, r.supportsUltracode("opus"))
	assert.False(t, r.supportsUltracode("missing"), "absent model is not trusted for ultracode")
	assert.True(t, r.supports("opus", "xhigh"))
	assert.True(t, r.supports("mystery", "xhigh"), "unknown model is trusted, no panic on nil entries")
}

// TestEffortResolver_StaticFallbackTrustsFilteredModel covers S2: the per-agent
// resolver falls back to the static catalog for a model the live CLI dropped from
// the dynamic list, so the decode, live-update, and startup paths all keep agreeing
// that a still-running filtered model (e.g. opus reported in unavailable_models) is
// ultracode-capable instead of silently downgrading it. The genuine downgrade case
// (model still listed, minus xhigh) still wins because dynamic takes precedence.
func TestEffortResolver_StaticFallbackTrustsFilteredModel(t *testing.T) {
	// Dynamic catalog lists only sonnet -> opus is "filtered".
	filtered := &ClaudeCodeAgent{availableModels: convertClaudeModels([]claudeCodeModelInfo{
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
	}, nil)}
	r := filtered.effortResolver()
	assert.True(t, r.supportsUltracode("opus"), "filtered opus resolves via the static fallback")
	assert.Equal(t, EffortUltracode, r.resolveEffort("opus", EffortUltracode), "filtered opus keeps ultracode")
	// A live edit echoing the current ultracode must NOT strip it for the filtered model.
	assert.Empty(t, r.updateFlagSettings("opus", EffortUltracode, EffortUltracode),
		"an unrelated live edit preserves a filtered model's ultracode")

	// Dynamic precedence: a model the live CLI lists WITHOUT xhigh is genuinely not
	// ultracode-capable even though the static catalog says it is.
	dropped := &ClaudeCodeAgent{availableModels: convertClaudeModels([]claudeCodeModelInfo{
		{Value: "opus", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
	}, nil)}
	assert.False(t, dropped.effortResolver().supportsUltracode("opus"),
		"a live CLI that dropped xhigh wins over the static fallback")
}

// TestEffortResolver_ContextWindowFallsBackToStatic verifies contextWindow resolves
// over the dynamic catalog first and the static catalog as a per-entry fallback --
// mirroring definedEfforts -- so a model the live CLI dropped from its list but the
// session is still running keeps its known window instead of reporting "unknown".
func TestEffortResolver_ContextWindowFallsBackToStatic(t *testing.T) {
	// Dynamic catalog lists only sonnet (200K); opus[1m] is "filtered".
	r := (&ClaudeCodeAgent{availableModels: convertClaudeModels([]claudeCodeModelInfo{
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
	}, nil)}).effortResolver()

	assert.Equal(t, int64(200_000), r.contextWindow("sonnet"), "dynamic entry wins")
	assert.Equal(t, int64(1_000_000), r.contextWindow("opus[1m]"),
		"filtered opus[1m] resolves its 1M window via the static fallback")
	assert.Equal(t, int64(0), r.contextWindow(DefaultModelSentinel),
		"the unresolved sentinel has no window in either catalog")
	assert.Equal(t, int64(0), r.contextWindow("ghost"),
		"a model absent from both catalogs is unknown")
}

// TestEffortResolver_SentinelIsUnresolved covers S1: the account-default sentinel is
// treated as unresolved by definedEfforts, so a session stuck on "default" (the CLI
// never echoed a concrete applied.model) passes its effort through rather than
// clamping it against the sentinel's empty effort list.
func TestEffortResolver_SentinelIsUnresolved(t *testing.T) {
	r := (&ClaudeCodeAgent{}).effortResolver() // static fallback only
	_, known := r.definedEfforts(DefaultModelSentinel)
	assert.False(t, known, "the sentinel is reported as unresolved, not known-with-empty-efforts")
	assert.Equal(t, "max", r.resolveEffort(DefaultModelSentinel, "max"), "effort passes through, not clamped to high")
	assert.False(t, r.unsupportedUltracode("max", DefaultModelSentinel))
	// An effort-only live edit on a stuck sentinel pushes NO effort delta: the sentinel
	// has no concrete model to resolve an effort against, so the live path emits nothing
	// (mirroring the launch path's omitted --effort), and the effort settles when the
	// model resolves. Pushing an effortLevel against the placeholder risks a level the
	// CLI's resolved model can't run.
	assert.Nil(t, r.updateFlagSettings(DefaultModelSentinel, "max", "high"))
}

// TestEffortResolver_EmptyDynamicEntryDefersToFallback covers E4: a KNOWN model the
// live CLI reports with no recognizable efforts -- supportsEffort:false, or only
// unrecognized level names (schema drift) -- lands in the dynamic catalog with an
// empty effort list. That empty entry must NOT shadow the populated static-fallback
// entry for the same model, or the user's stored effort would be silently downgraded
// post-init. definedEfforts prefers a populated entry, so the fallback's menu wins.
func TestEffortResolver_EmptyDynamicEntryDefersToFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		cli  claudeCodeModelInfo
	}{
		{"supportsEffort false", claudeCodeModelInfo{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: false}},
		{"only unrecognized levels", claudeCodeModelInfo{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"ultra", "blazing"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := convertClaudeModels([]claudeCodeModelInfo{tc.cli}, nil)
			// Precondition: the dynamic sonnet entry really is present-but-empty.
			dynSonnet := FindAvailableModel(dyn, "sonnet")
			require.NotNil(t, dynSonnet)
			require.Empty(t, dynSonnet.SupportedEfforts)

			r := (&ClaudeCodeAgent{availableModels: dyn}).effortResolver()
			efforts, known := r.definedEfforts("sonnet")
			assert.True(t, known, "sonnet is known")
			assert.NotEmpty(t, efforts, "empty dynamic entry defers to the populated static fallback")
			assert.True(t, r.supports("sonnet", "max"), "the fallback menu rescues a recognized level")
			assert.Equal(t, "max", r.resolveEffort("sonnet", "max"), "stored effort is not downgraded")
			assert.False(t, r.launchOmitsEffort("sonnet", "max"), "sonnet is not treated as effort-less")
		})
	}
}

// TestEffortResolver_GenuinelyEffortlessModelStaysEmpty is E4's negative case: a model
// effort-less in BOTH catalogs (Haiku) stays known-with-no-efforts -- the
// populated-entry preference must not invent a menu for it -- so the launch path still
// omits --effort (Haiku does not accept the flag).
func TestEffortResolver_GenuinelyEffortlessModelStaysEmpty(t *testing.T) {
	dyn := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "haiku", DisplayName: "Haiku", SupportsEffort: false},
	}, nil)
	r := (&ClaudeCodeAgent{availableModels: dyn}).effortResolver()
	efforts, known := r.definedEfforts("haiku")
	assert.True(t, known, "haiku is known")
	assert.Empty(t, efforts, "haiku has no efforts in either catalog")
	assert.True(t, r.launchOmitsEffort("haiku", "high"), "an effort-less model still omits --effort")
}

// TestClaudeFallbackDisplayName covers S6: a "[1m]" model id with no CLI displayName
// renders as "X (1M context)" instead of the raw "X[1m]".
func TestClaudeFallbackDisplayName(t *testing.T) {
	assert.Equal(t, "Opus (1M context)", claudeFallbackDisplayName("opus[1m]"))
	assert.Equal(t, "Opus", claudeFallbackDisplayName("opus"))
	assert.Equal(t, "Fable (1M context)", claudeFallbackDisplayName("fable[1m]"))
	// A decorated 1M marker is sized at 1M by claudeContextWindowForValue, so the
	// fallback name must agree (detect via is1MContextVariant, not a literal "[1m]"
	// suffix) instead of falling through to a garbled "Fable[1m Beta]".
	assert.Equal(t, "Fable (1M context)", claudeFallbackDisplayName("fable[1m-beta]"))
	assert.Equal(t, "Opus (1M context)", claudeFallbackDisplayName("opus[1m-preview]"))
	require.True(t, is1MContextVariant("fable[1m-beta]"),
		"guards the predicate the display name now relies on")
	// A bracket whose content is not a 1M marker is not treated as a 1M variant.
	require.False(t, is1MContextVariant("opus[preview]"))
	assert.NotContains(t, claudeFallbackDisplayName("opus[preview]"), "1M context")
	// Via the full conversion, with the CLI omitting displayName.
	got := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "opus[1m]", SupportsEffort: true, SupportedEffortLevels: []string{"high", "xhigh"}},
	}, nil))["opus[1m]"]
	require.NotNil(t, got)
	assert.Equal(t, "Opus (1M context)", got.DisplayName, "missing displayName falls back to the [1m]-aware name")
}

// Canonical CLI effort-level vectors the model fixtures reuse: xhigh-capable models
// (Opus/Fable) report low..max, while max-only models (Sonnet) omit xhigh. They are
// read-only -- convertClaudeModels only reads SupportedEffortLevels -- so sharing the
// slices across fixtures is safe.
var (
	claudeXHighLevels = []string{"low", "medium", "high", "xhigh", "max"}
	claudeMaxLevels   = []string{"low", "medium", "high", "max"}
)

// realCLIModelsNoOpus mirrors the actual Claude Code 2.1.170 initialize `models`
// array on an account whose default resolves to Opus: the CLI lists the account
// default sentinel, Fable, Sonnet, Sonnet (1M), and Haiku -- but NO opus/opus[1m]
// entry. Opus is reachable only behind "default". The settings-refresh path then
// settles a.model onto "opus[1m]" (normalized from get_settings' concrete
// "claude-opus-4-8[1m]"), a model absent from this list.
func realCLIModelsNoOpus() []claudeCodeModelInfo {
	return []claudeCodeModelInfo{
		{Value: "default", DisplayName: "Default (recommended)", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		{Value: "claude-fable-5[1m]", DisplayName: "Fable", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: claudeMaxLevels},
		{Value: "sonnet[1m]", DisplayName: "Sonnet (1M context)", SupportsEffort: true, SupportedEffortLevels: claudeMaxLevels},
		{Value: "haiku", DisplayName: "Haiku", SupportsEffort: false},
	}
}

// TestEnsureSettledModelListed_InjectsResolvedDefault is the regression for the
// account-default-resolves-to-an-unlisted-model bug: the picker rendered
// a.availableModels verbatim, so when the "default" sentinel resolved to opus[1m]
// (a model the CLI exposes only behind "default") the settings panel showed no Opus
// row, left it unselected, and hid its effort menu. ensureSettledModelListed adds
// the resolved model so all three are fixed from the backend.
func TestEnsureSettledModelListed_InjectsResolvedDefault(t *testing.T) {
	a := &ClaudeCodeAgent{
		model:           "opus[1m]",
		availableModels: convertClaudeModels(realCLIModelsNoOpus(), nil),
	}

	// Reproduces the bug: the settled model is absent from the picker catalog.
	require.Nil(t, FindAvailableModel(a.availableModels, "opus[1m]"),
		"precondition: the CLI list omits opus[1m], so the picker can't show it")

	a.ensureSettledModelListed()

	got := FindAvailableModel(a.availableModels, "opus[1m]")
	require.NotNil(t, got, "the resolved default is added to the picker catalog")
	assert.Equal(t, "Opus (1M context)", got.DisplayName, "named from the static fallback, not the raw id")
	assert.Equal(t, int64(claudeOneMillionContextWindow), got.ContextWindow)
	// The injected entry carries Opus's real effort menu (xhigh + ultracode), so the
	// settings panel renders an effort section instead of hiding it.
	assert.Equal(t, []string{"auto", "ultracode", "max", "xhigh", "high", "medium", "low"}, claudeEffortIDs(got),
		"the injected entry exposes Opus's full effort menu")
	// The sentinel and the originally-listed models survive untouched.
	assert.NotNil(t, FindAvailableModel(a.availableModels, DefaultModelSentinel))
	assert.NotNil(t, FindAvailableModel(a.availableModels, "sonnet[1m]"))
	// The resolved model lands in its CANONICAL slot: after Fable and before Sonnet,
	// matching the static catalog's most->least-powerful order -- NOT jammed right
	// after the sentinel (ahead of Fable), which is what a naive insert produced.
	ids := make([]string, len(a.availableModels))
	for i, m := range a.availableModels {
		ids[i] = m.GetId()
	}
	assert.Equal(t, []string{DefaultModelSentinel, "fable[1m]", "opus[1m]", "sonnet", "sonnet[1m]", "haiku"}, ids,
		"opus[1m] is inserted after Fable, in canonical picker order")
}

// TestEnsureSettledModelListed_AppendsLowestRankAtEnd exercises the insertAt=len(...)
// default branch: when the resolved model out-ranks (is lower priority than) NONE of
// the already-listed models, the canonical-rank scan finds no insertion point and the
// model must append at the very end. InjectsResolvedDefault inserts mid-list; this
// covers the opposite boundary. The CLI list is truncated to the top two entries
// (default, fable[1m]) so opus[1m] -- the real injectable, rank 3 -- has no
// lower-priority neighbor to slot before.
func TestEnsureSettledModelListed_AppendsLowestRankAtEnd(t *testing.T) {
	a := &ClaudeCodeAgent{
		model:           "opus[1m]",
		availableModels: convertClaudeModels(realCLIModelsNoOpus()[:2], nil),
	}
	require.Nil(t, FindAvailableModel(a.availableModels, "opus[1m]"),
		"precondition: the truncated CLI list omits opus[1m]")

	a.ensureSettledModelListed()

	ids := make([]string, len(a.availableModels))
	for i, m := range a.availableModels {
		ids[i] = m.GetId()
	}
	assert.Equal(t, []string{DefaultModelSentinel, "fable[1m]", "opus[1m]"}, ids,
		"a model lower-ranked than every listed model appends at the end, not mid-list")
}

// TestEnsureSettledModelListed_NoOps covers the cases that must leave the catalog
// byte-for-byte unchanged: an already-listed model, the unresolved sentinel/empty
// model, and the old-CLI empty list (where mutating would REPLACE the static
// fallback with a singleton).
func TestEnsureSettledModelListed_NoOps(t *testing.T) {
	t.Run("already listed", func(t *testing.T) {
		a := &ClaudeCodeAgent{model: "sonnet", availableModels: convertClaudeModels(realCLIModelsNoOpus(), nil)}
		n := len(a.availableModels)
		a.ensureSettledModelListed()
		assert.Len(t, a.availableModels, n, "a listed model is not appended again")
	})

	for _, tc := range []struct{ name, model string }{
		{"empty model", ""},
		{"unresolved sentinel", DefaultModelSentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &ClaudeCodeAgent{model: tc.model, availableModels: convertClaudeModels(realCLIModelsNoOpus(), nil)}
			n := len(a.availableModels)
			a.ensureSettledModelListed()
			assert.Len(t, a.availableModels, n, "an unresolved/sentinel model is not injected")
		})
	}

	t.Run("empty dynamic list (old CLI)", func(t *testing.T) {
		a := &ClaudeCodeAgent{model: "opus[1m]", availableModels: nil}
		a.ensureSettledModelListed()
		assert.Empty(t, a.availableModels,
			"an empty list stays empty so effortCatalog keeps falling back to the full static catalog")
	})
}

// TestEnsureSettledModelListed_UnknownModelNotInjected is the guard against the
// regression a naive "synthesize an entry for any settled model" would cause: a model
// in NEITHER catalog is deliberately left unlisted. Injecting an effort-less
// placeholder for it would flip effortResolver.definedEfforts from "unknown" (trust
// the CLI's report) to "known with no efforts" (clamp/downgrade), silently demoting a
// session the CLI is running at ultracode -- exactly what effortFromApplied/
// updateFlagSettings guard against. So injection must skip it AND leave the resolver's
// trust intact.
func TestEnsureSettledModelListed_UnknownModelNotInjected(t *testing.T) {
	a := &ClaudeCodeAgent{
		model:           "mystery[1m]",
		availableModels: convertClaudeModels(realCLIModelsNoOpus(), nil),
	}
	n := len(a.availableModels)

	a.ensureSettledModelListed()

	assert.Nil(t, FindAvailableModel(a.availableModels, "mystery[1m]"),
		"a model in neither catalog is left unlisted, not synthesized")
	assert.Len(t, a.availableModels, n, "no entry was inserted")

	// The resolver must still treat the unknown model as unknown -> trusts the CLI's
	// ultracode report rather than downgrading it. This is the property the no-inject
	// rule protects.
	r := a.effortResolver()
	_, known := r.definedEfforts("mystery[1m]")
	assert.False(t, known, "the unknown model stays unknown to the resolver")
	assert.Equal(t, EffortUltracode,
		r.effortFromApplied(ptrconv.Ptr("xhigh"), ptrconv.Ptr(true), EffortUltracode, "mystery[1m]"),
		"a running ultracode session on the unknown model is trusted, not downgraded")
}

// TestEnsureSettledModelListed_StaticModelKeepsResolverVerdict locks in that injecting
// a static-catalog model (the safe set) changes NO resolver verdict: the model was
// already known via the fallback, so before and after the insert the resolver returns
// the same efforts and the same ultracode trust. This is why the static-catalog
// restriction is correct -- it only surfaces in the picker what the resolver could
// already speak to.
func TestEnsureSettledModelListed_StaticModelKeepsResolverVerdict(t *testing.T) {
	a := &ClaudeCodeAgent{
		model:           "opus[1m]",
		availableModels: convertClaudeModels(realCLIModelsNoOpus(), nil),
	}

	before := a.effortResolver()
	beforeEfforts, beforeKnown := before.definedEfforts("opus[1m]")
	require.True(t, beforeKnown, "opus[1m] is already known via the static fallback before injection")

	a.ensureSettledModelListed()

	after := a.effortResolver()
	afterEfforts, afterKnown := after.definedEfforts("opus[1m]")
	assert.True(t, afterKnown)
	assert.Equal(t, claudeEffortIDsFromList(beforeEfforts), claudeEffortIDsFromList(afterEfforts),
		"the effort menu the resolver reports is unchanged by the picker insert")
	assert.True(t, after.supportsUltracode("opus[1m]"), "ultracode support is unchanged")
}

// TestCanonicalModelRank pins the helper's contract directly: catalog membership
// maps to the static index (most->least-powerful order), and a model the static
// catalog does NOT list ranks last (== len) so it sorts after every known model.
// The unknown->len branch is the one ensureSettledModelListed's tests never reach
// (an unknown model is dropped before any rank is taken), yet it is what keeps a
// future dynamic-only id from out-sorting a catalog-known model.
func TestCanonicalModelRank(t *testing.T) {
	assert.Equal(t, 0, canonicalModelRank(DefaultModelSentinel), "the sentinel ranks first")
	assert.Equal(t, 1, canonicalModelRank("fable[1m]"))
	assert.Equal(t, 3, canonicalModelRank("opus[1m]"))
	assert.Equal(t, 6, canonicalModelRank("haiku"), "the weakest catalog model ranks last among known ids")
	assert.Less(t, canonicalModelRank("fable[1m]"), canonicalModelRank("haiku"),
		"a more powerful model out-ranks a weaker one")
	assert.Equal(t, len(claudeCodeAvailableModels), canonicalModelRank("mystery[1m]"),
		"a model absent from the static catalog ranks last so it sorts after every known id")
}

func TestClaudeEffortFlagSettings(t *testing.T) {
	tests := []struct {
		name      string
		newEffort string
		curEffort string
		expected  map[string]interface{}
	}{
		{
			name:      "enable ultracode sets xhigh base + ultracode true",
			newEffort: "ultracode",
			curEffort: "high",
			expected:  map[string]interface{}{"effortLevel": "xhigh", "ultracode": true},
		},
		{
			name:      "leaving ultracode clears the ultracode flag",
			newEffort: "max",
			curEffort: "ultracode",
			expected:  map[string]interface{}{"effortLevel": "max", "ultracode": false},
		},
		{
			name:      "ordinary effort change carries no ultracode key",
			newEffort: "high",
			curEffort: "medium",
			expected:  map[string]interface{}{"effortLevel": "high"},
		},
		{
			name:      "ultracode unchanged is a no-op",
			newEffort: "ultracode",
			curEffort: "ultracode",
			expected:  nil,
		},
		{
			name:      "auto transition is handled elsewhere (no flag settings)",
			newEffort: "auto",
			curEffort: "ultracode",
			expected:  nil,
		},
		{
			name:      "empty effort is a no-op",
			newEffort: "",
			curEffort: "high",
			expected:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, claudeEffortFlagSettings(tt.newEffort, tt.curEffort))
		})
	}
}

func TestClaudeEffortUpdateFlagSettings(t *testing.T) {
	tests := []struct {
		name        string
		targetModel string
		newEffort   string
		curEffort   string
		expected    map[string]interface{}
	}{
		{
			// Model-only change off opus+ultracode onto sonnet: no effort delta,
			// but the stale ultracode boolean must be cleared AND the stale
			// effortLevel pinned to a sonnet-supported level. Clearing only the
			// boolean would leave the CLI at the ultracode base "xhigh" (which the
			// CLI does not re-resolve on a model-only change), so we also pin
			// effortLevel to xhigh resolved for sonnet -> "high" (its default).
			name:        "model-only switch leaving ultracode for an unsupported model clears the flag and pins a safe effort",
			targetModel: "sonnet",
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    map[string]interface{}{"effortLevel": "high", "ultracode": false},
		},
		{
			// Model-only change to another ultracode-capable model keeps the
			// combo: nothing to send (the model key is added by the caller).
			name:        "model-only switch between ultracode-capable models is a no-op",
			targetModel: "opus[1m]",
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    nil,
		},
		{
			// Combined change already routes through claudeEffortFlagSettings,
			// which clears ultracode when leaving it; the guard is idempotent.
			name:        "combined model+effort leaving ultracode clears the flag once",
			targetModel: "sonnet",
			newEffort:   "high",
			curEffort:   "ultracode",
			expected:    map[string]interface{}{"effortLevel": "high", "ultracode": false},
		},
		{
			// Requesting ultracode on a model that can't run it downgrades the
			// level to high and never sets the ultracode boolean true (sent here
			// because high differs from the current "medium").
			name:        "ultracode requested on unsupported model downgrades without setting the flag",
			targetModel: "sonnet",
			newEffort:   "ultracode",
			curEffort:   "medium",
			expected:    map[string]interface{}{"effortLevel": "high"},
		},
		{
			name:        "enabling ultracode on a supported model sets the combo",
			targetModel: "opus",
			newEffort:   "ultracode",
			curEffort:   "high",
			expected:    map[string]interface{}{"effortLevel": "xhigh", "ultracode": true},
		},
		{
			name:        "ordinary effort change is unaffected",
			targetModel: "sonnet",
			newEffort:   "max",
			curEffort:   "high",
			expected:    map[string]interface{}{"effortLevel": "max"},
		},
		{
			name:        "no requested effort and not leaving ultracode is a no-op",
			targetModel: "sonnet",
			newEffort:   "",
			curEffort:   "high",
			expected:    nil,
		},
		{
			// An unknown model (in NEITHER the dynamic nor the static catalog -- e.g.
			// one the live CLI filtered into unavailable_models but the session is still
			// running) at ultracode is TRUSTED, mirroring effortFromApplied/
			// trustCLIUltracodeReport: the CLI is the authority on what it actually
			// applied, so a model-only or unrelated live update must NOT strip the
			// ultracode boolean just because the catalog can't confirm xhigh support.
			// Without the `known &&` gate this would emit {ultracode:false,
			// effortLevel:"xhigh"} and silently downgrade a session the CLI is happily
			// running at ultracode, contradicting the decode side that trusts the same
			// model. The two paths must agree.
			name:        "model-only update on an unknown model running ultracode preserves the flag",
			targetModel: "ghost",
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, newEffortResolver(claudeCodeAvailableModels).updateFlagSettings(tt.targetModel, tt.newEffort, tt.curEffort))
		})
	}
}

func TestClaudeEffortFromApplied(t *testing.T) {
	tests := []struct {
		name      string
		applied   *string
		ultracode *bool
		curEffort string
		model     string
		want      string
	}{
		{
			name:      "opus ultracode:true maps xhigh-base back to ultracode",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     "opus",
			want:      "ultracode",
		},
		{
			name:      "opus[1m] ultracode:true maps to ultracode",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "ultracode",
			model:     "opus[1m]",
			want:      "ultracode",
		},
		{
			name:      "sonnet ultracode:true is ignored - model cannot run it",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "high",
			model:     "sonnet",
			want:      "xhigh", // keep the reported level; never mislabel Sonnet as ultracode
		},
		{
			// S1: a model the catalog does NOT know (e.g. the CLI reported it in
			// unavailable_models, so convertClaudeModels filtered it) is trusted when
			// the CLI confirms ultracode:true -- we don't relabel a running ultracode
			// session to xhigh just because its model dropped out of the catalog. This
			// differs from Sonnet above, which the catalog KNOWS lacks ultracode.
			name:      "unknown model ultracode:true is trusted - CLI is the authority",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     "claude-future-preview",
			want:      "ultracode",
		},
		{
			// S2: the account-default sentinel is the one unknown model NOT trusted
			// for ultracode. A session stuck on the literal "default" (the CLI never
			// echoed a concrete applied.model) has no real model behind it, so a CLI
			// ultracode:true report passes the reported effort through instead of
			// minting a phantom "ultracode" against the placeholder.
			name:      "stuck sentinel ultracode:true is not promoted - no model behind it",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     DefaultModelSentinel,
			want:      "xhigh",
		},
		{
			name:      "unentitled opus reports ultracode:false + xhigh - graceful downgrade",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(false),
			curEffort: "ultracode",
			model:     "opus",
			want:      "xhigh",
		},
		{
			name:      "ultracode cleared with omitted effort falls back to xhigh base",
			applied:   nil,
			ultracode: ptrconv.Ptr(false),
			curEffort: "ultracode",
			model:     "opus",
			want:      "xhigh",
		},
		{
			name:      "ultracode:false with non-ultracode current is unchanged",
			applied:   nil,
			ultracode: ptrconv.Ptr(false),
			curEffort: "high",
			model:     "opus",
			want:      "high",
		},
		{
			name:      "nil ultracode passes the reported effort through",
			applied:   ptrconv.Ptr("max"),
			ultracode: nil,
			curEffort: "high",
			model:     "opus",
			want:      "max",
		},
		{
			name:      "nil applied effort retains current effort",
			applied:   nil,
			ultracode: nil,
			curEffort: "medium",
			model:     "sonnet",
			want:      "medium",
		},
		{
			// CLI omits both fields (e.g. a model switch that didn't touch
			// effort) while curEffort is still "ultracode" on a model that
			// can't run it: the guard must clear the stale value to xhigh
			// rather than mislabel a Sonnet session as ultracode.
			name:      "stale ultracode on a model that lost support is cleared",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     "sonnet",
			want:      "xhigh",
		},
		{
			// Same shape, but Opus genuinely supports ultracode, so the guard
			// must NOT clear it.
			name:      "stale ultracode on opus with omitted fields is retained",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     "opus",
			want:      "ultracode",
		},
		{
			// A model unknown to BOTH catalogs with a stale curEffort=="ultracode"
			// and the CLI omitting the ultracode field is LEFT ALONE: the final
			// clear-stale guard is gated on `known`, so an unknown model keeps its
			// value for the same "trust the CLI / can't confirm it lacks ultracode"
			// reason that promotes an unknown model's ultracode:true report. The
			// launch flags and startup reconcile never actually drive an unknown
			// model into ultracode (both downgrade it over the static catalog), so
			// the CLI side stays safe; only the stored/broadcast label reads
			// "ultracode" until a concrete CLI report overrides it.
			name:      "unknown model with omitted fields retains stale ultracode",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     "claude-future-preview",
			want:      "ultracode",
		},
		{
			// Defensive: the CLI reports applied.effort as a concrete enum or
			// null, never "". An unexpected empty string must be treated like
			// omitted (retain curEffort) rather than blanking the effort to "".
			name:      "empty applied effort is treated as omitted (retains curEffort)",
			applied:   ptrconv.Ptr(""),
			ultracode: nil,
			curEffort: "high",
			model:     "opus",
			want:      "high",
		},
		{
			// Empty applied.effort with curEffort=="ultracode" on opus: the ""
			// is ignored, so the ultracode value survives (opus supports it)
			// instead of being blanked.
			name:      "empty applied effort retains stale ultracode on opus",
			applied:   ptrconv.Ptr(""),
			ultracode: nil,
			curEffort: "ultracode",
			model:     "opus",
			want:      "ultracode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newEffortResolver(claudeCodeAvailableModels).effortFromApplied(tt.applied, tt.ultracode, tt.curEffort, tt.model))
		})
	}
}

func TestBuildStartupFlagSettings_Ultracode(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		effort        string
		wantUltracode bool
	}{
		{"fable[1m] ultracode enables the combo", "fable[1m]", EffortUltracode, true},
		{"opus ultracode enables the combo", "opus", EffortUltracode, true},
		{"opus[1m] ultracode enables the combo", "opus[1m]", EffortUltracode, true},
		{"sonnet ultracode is not enabled (unsupported)", "sonnet", EffortUltracode, false},
		{"haiku ultracode is not enabled (unsupported)", "haiku", EffortUltracode, false},
		{"unknown model ultracode is not enabled", "claude-future-preview", EffortUltracode, false},
		{"opus non-ultracode adds no effort keys", "opus", "xhigh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &ClaudeCodeAgent{model: tt.model, effort: tt.effort}
			fs := a.buildStartupFlagSettings(nil)
			if tt.wantUltracode {
				assert.Equal(t, "xhigh", fs["effortLevel"])
				assert.Equal(t, true, fs["ultracode"])
			} else {
				_, hasUltra := fs["ultracode"]
				assert.False(t, hasUltra, "ultracode key should be absent")
				_, hasLevel := fs["effortLevel"]
				assert.False(t, hasLevel, "effortLevel key should be absent for non-ultracode startup")
			}
		})
	}
}

func TestModelSupportsUltracode(t *testing.T) {
	// Against the static catalog: the xhigh-capable models (Fable, Opus) list
	// ultracode; Sonnet/Haiku do not. Unlike supports, unknown models are NOT trusted.
	static := newEffortResolver(claudeCodeAvailableModels)
	assert.True(t, static.supportsUltracode("fable[1m]"))
	assert.True(t, static.supportsUltracode("opus"))
	assert.True(t, static.supportsUltracode("opus[1m]"))
	assert.False(t, static.supportsUltracode("sonnet"))
	assert.False(t, static.supportsUltracode("sonnet[1m]"))
	assert.False(t, static.supportsUltracode("haiku"))
	assert.False(t, static.supportsUltracode("claude-future-preview"), "unknown models are not trusted for ultracode")
	assert.False(t, static.supportsUltracode(""))

	// Against a dynamic catalog: a model the static catalog never heard of is
	// ultracode-capable when the live CLI advertises xhigh for it (this is what
	// makes "auto from xhigh" work for future models without a code change), and
	// not otherwise.
	dynamic := newEffortResolver(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "mythos", DisplayName: "Mythos 6", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{Value: "sprite", DisplayName: "Sprite 1", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high"}},
	}, nil))
	assert.True(t, dynamic.supportsUltracode("mythos"), "xhigh-capable dynamic model is ultracode-capable")
	assert.False(t, dynamic.supportsUltracode("sprite"), "non-xhigh dynamic model is not ultracode-capable")
	assert.False(t, dynamic.supportsUltracode("opus"), "model absent from the dynamic catalog is not trusted")
}

func TestResolveClaudeEffortForModel(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		effort string
		want   string
	}{
		{"fable[1m] keeps ultracode", "fable[1m]", "ultracode", "ultracode"},
		{"opus keeps ultracode", "opus", "ultracode", "ultracode"},
		{"opus[1m] keeps ultracode", "opus[1m]", "ultracode", "ultracode"},
		{"sonnet downgrades ultracode to high", "sonnet", "ultracode", "high"},
		{"haiku downgrades ultracode to high", "haiku", "ultracode", "high"},
		{"unknown downgrades ultracode to high", "claude-future-preview", "ultracode", "high"},
		{"sonnet downgrades xhigh to high", "sonnet", "xhigh", "high"},
		{"opus keeps xhigh", "opus", "xhigh", "xhigh"},
		{"unknown trusts xhigh", "claude-future-preview", "xhigh", "xhigh"},
		{"supported effort passes through", "sonnet", "high", "high"},
		{"auto passes through", "sonnet", "auto", "auto"},
		{"empty passes through", "opus", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newEffortResolver(claudeCodeAvailableModels).resolveEffort(tt.model, tt.effort))
		})
	}
}

// claudeEffortIDs returns the ordered effort IDs of a model, for catalog assertions.
func claudeEffortIDs(m *ModelInfo) []string {
	return claudeEffortIDsFromList(m.SupportedEfforts)
}

func claudeEffortIDsFromList(efforts []*EffortInfo) []string {
	ids := make([]string, 0, len(efforts))
	for _, e := range efforts {
		ids = append(ids, e.Id)
	}
	return ids
}

func TestConvertClaudeModels(t *testing.T) {
	models := []claudeCodeModelInfo{
		{Value: "default", DisplayName: "Default (recommended)", Description: "the account default", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		// A bare "fable" and an explicit "fable[1m]" both normalize to the canonical
		// "fable[1m]" id, so they collapse to one entry (first, effort-bearing, wins).
		{Value: "fable", DisplayName: "Fable 5", Description: "Most powerful for the hardest problems", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		{Value: "fable[1m]", DisplayName: "Fable 5 (1M context)", Description: "Most powerful for the hardest problems", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		{Value: "opus", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: claudeMaxLevels},
		{Value: "haiku", DisplayName: "Haiku", SupportsEffort: false},
		{Value: "zdr-blocked", DisplayName: "Blocked", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
		{Value: "internal-preview", DisplayName: "Internal", Disabled: true, SupportsEffort: true, SupportedEffortLevels: claudeXHighLevels},
	}
	unavailable := []claudeCodeModelInfo{{Value: "zdr-blocked"}}

	got := convertClaudeModels(models, unavailable)

	// The disabled entry and the unavailable entry are dropped; the "default"
	// sentinel IS surfaced (a selectable "let the CLI pick" option) and keeps
	// its leading CLI order.
	var ids []string
	for _, m := range got {
		ids = append(ids, m.Id)
	}
	assert.Equal(t, []string{"default", "fable[1m]", "opus", "sonnet", "haiku"}, ids)

	byID := claudeModelsByID(got)
	xhighEfforts := []string{"auto", "ultracode", "max", "xhigh", "high", "medium", "low"}

	// The "default" sentinel is a selectable entry, but carries NO efforts or
	// context window even when the CLI reports them (input sets supportsEffort +
	// xhigh levels) -- those belong to the concrete model it resolves to. The
	// manager (not convert) gives it the IsDefault badge.
	def := byID["default"]
	require.NotNil(t, def)
	assert.Equal(t, "Default (recommended)", def.DisplayName)
	assert.Equal(t, "the account default", def.Description, "sentinel keeps its CLI-provided description")
	assert.Empty(t, def.SupportedEfforts, "default sentinel must not expose an effort menu")
	assert.Equal(t, "", def.DefaultEffort)
	assert.Equal(t, int64(0), def.ContextWindow, "default sentinel has no context window until resolved")
	assert.False(t, def.IsDefault, "convert leaves IsDefault for the manager to set")

	// Fable: full xhigh+ultracode menu, xhigh default, 1M window. Canonical id is
	// "fable[1m]"; the bare-"fable" input collapsed into it (dedup, first wins, so
	// the display name is "Fable 5" not the duplicate's "Fable 5 (1M context)").
	require.NotContains(t, byID, "fable", "bare fable normalizes to fable[1m]")
	fable := byID["fable[1m]"]
	require.NotNil(t, fable)
	assert.Equal(t, "Fable 5", fable.DisplayName)
	assert.Equal(t, "Most powerful for the hardest problems", fable.Description)
	assert.Equal(t, xhighEfforts, claudeEffortIDs(fable))
	assert.Equal(t, "xhigh", fable.DefaultEffort)
	assert.Equal(t, int64(1_000_000), fable.ContextWindow)
	assert.False(t, fable.IsDefault, "convert leaves IsDefault for the manager to set")

	// Sonnet: max but no xhigh ⇒ no ultracode, high default.
	sonnet := byID["sonnet"]
	require.NotNil(t, sonnet)
	assert.Equal(t, []string{"auto", "max", "high", "medium", "low"}, claudeEffortIDs(sonnet))
	assert.Equal(t, "high", sonnet.DefaultEffort)

	// Haiku: no effort support ⇒ no selector, no default effort.
	haiku := byID["haiku"]
	require.NotNil(t, haiku)
	assert.Empty(t, haiku.SupportedEfforts)
	assert.Equal(t, "", haiku.DefaultEffort)
	assert.Equal(t, int64(200_000), haiku.ContextWindow)

	// Falls back to a missing display name from the id.
	assert.Equal(t, "Sonnet", byID["sonnet"].DisplayName)

	// Empty input ⇒ nil so AvailableModels() falls back to the static catalog.
	assert.Nil(t, convertClaudeModels(nil, nil))
}

func TestClaudeContextWindowForValue(t *testing.T) {
	assert.Equal(t, int64(1_000_000), claudeContextWindowForValue("opus[1m]"))
	assert.Equal(t, int64(1_000_000), claudeContextWindowForValue("claude-fable-5[1m]"))
	assert.Equal(t, int64(1_000_000), claudeContextWindowForValue("opus[1M]"), "case-insensitive")
	// A decorated 1M marker (a future beta/preview label on the bracketed suffix) still
	// resolves to the 1M window rather than silently dropping to 200K.
	assert.Equal(t, int64(1_000_000), claudeContextWindowForValue("opus[1m-beta]"))
	assert.Equal(t, int64(1_000_000), claudeContextWindowForValue("claude-opus-4-8[1M-preview]"))
	assert.Equal(t, int64(200_000), claudeContextWindowForValue("opus"))
	assert.Equal(t, int64(200_000), claudeContextWindowForValue("default"))
	assert.Equal(t, int64(200_000), claudeContextWindowForValue("haiku"))
	// A stray "1m" that is not a trailing bracket group must not false-positive.
	assert.Equal(t, int64(200_000), claudeContextWindowForValue("1m-context"))
	assert.Equal(t, int64(200_000), claudeContextWindowForValue("opus[1m]x"))
}

// TestConvertClaudeModels_NormalizesFullIdValues locks the fix for the
// catalog-id/a.model mismatch: the CLI reports the account-resolved model's
// `value` as a fully-qualified id (verified against the live CLI:
// "claude-fable-5[1m]"), while refreshSettingsFromAgent stores
// normalizeClaudeCodeModel(applied.model) = "fable[1m]". convertClaudeModels must
// normalize the value so a running model matches its own catalog entry (efforts,
// ultracode, display name, frontend effort selector all key off the id).
func TestConvertClaudeModels_NormalizesFullIdValues(t *testing.T) {
	got := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "claude-fable-5[1m]", DisplayName: "Fable", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{Value: "claude-sonnet-4-6", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
	}, nil))

	// Fully-qualified values collapse to the alias space a.model uses.
	require.Contains(t, got, "fable[1m]")
	require.NotContains(t, got, "claude-fable-5[1m]")
	require.Contains(t, got, "sonnet")
	assert.Equal(t, int64(1_000_000), got["fable[1m]"].ContextWindow, "[1m] suffix survives normalization")
	assert.True(t, effortListContains(got["fable[1m]"].SupportedEfforts, EffortUltracode), "normalized Fable keeps its xhigh⇒ultracode menu")
	assert.Equal(t, int64(200_000), got["sonnet"].ContextWindow)
}

// TestConvertClaudeModels_DedupAndEffortOrdering covers two robustness fixes:
// duplicate ids collapse to one entry, and a model's effort menu is ordered by
// rank regardless of the order the CLI reports the levels in (unknown levels
// dropped).
func TestConvertClaudeModels_DedupAndEffortOrdering(t *testing.T) {
	got := convertClaudeModels([]claudeCodeModelInfo{
		// Two entries that normalize to the same id collapse to one; the first
		// (effort-bearing) wins. Its levels are reported out of order.
		{Value: "claude-fable-5[1m]", DisplayName: "Fable", SupportsEffort: true, SupportedEffortLevels: []string{"max", "low", "xhigh", "high", "medium"}},
		{Value: "fable[1m]", DisplayName: "Fable dup"},
		// Scrambled levels, no xhigh ⇒ no ultracode.
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"high", "low", "max", "medium"}},
		// An unrecognized level is dropped rather than panicking or appearing.
		{Value: "probemodel", DisplayName: "Probe", SupportsEffort: true, SupportedEffortLevels: []string{"medium", "ludicrous", "low"}},
	}, nil)

	byID := claudeModelsByID(got)
	require.Len(t, got, 3, "duplicate fable[1m] collapses")
	assert.Equal(t, []string{"auto", "ultracode", "max", "xhigh", "high", "medium", "low"}, claudeEffortIDs(byID["fable[1m]"]), "out-of-order xhigh levels sorted strongest→weakest")
	assert.Equal(t, []string{"auto", "max", "high", "medium", "low"}, claudeEffortIDs(byID["sonnet"]))
	assert.Equal(t, []string{"auto", "medium", "low"}, claudeEffortIDs(byID["probemodel"]), "unknown 'ludicrous' level dropped")
}

// TestConvertClaudeModels_UnavailableSkipNormalized verifies the unavailable_models
// filter matches on normalized id, so an entry reported in unavailable_models under
// a different spelling than its models-array counterpart (one fully-qualified, one
// aliased) is still dropped rather than leaking through as selectable.
func TestConvertClaudeModels_UnavailableSkipNormalized(t *testing.T) {
	got := claudeModelsByID(convertClaudeModels(
		[]claudeCodeModelInfo{
			// models lists the alias; unavailable_models lists the fully-qualified
			// value (and vice-versa). Both must be filtered.
			{Value: "fable[1m]", DisplayName: "Fable", SupportsEffort: true, SupportedEffortLevels: []string{"xhigh"}},
			{Value: "claude-opus-4-8", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
			{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
		},
		[]claudeCodeModelInfo{
			{Value: "claude-fable-5[1m]"}, // fully-qualified form of fable[1m]
			{Value: "opus"},               // aliased form of claude-opus-4-8
		},
	))

	require.NotContains(t, got, "fable[1m]", "unavailable reported as claude-fable-5[1m] still filters fable[1m]")
	require.NotContains(t, got, "opus", "unavailable reported as opus still filters claude-opus-4-8")
	require.Contains(t, got, "sonnet")
	assert.Len(t, got, 1)
}

// TestClaudeSupportedEfforts_OnlyUnknownLevels verifies a model that claims effort
// support but lists only levels we don't recognize gets no effort menu (nil), not a
// lone "auto" stub -- so the UI hides the selector instead of showing a useless one.
func TestClaudeSupportedEfforts_OnlyUnknownLevels(t *testing.T) {
	got := claudeSupportedEfforts(normalizedEffortLevelSet(claudeCodeModelInfo{
		Value: "probe", SupportsEffort: true, SupportedEffortLevels: []string{"ludicrous", "plaid"},
	}))
	assert.Nil(t, got, "all-unrecognized levels ⇒ no effort menu")

	// And via the full conversion: the model surfaces with no effort selector.
	m := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "probe", DisplayName: "Probe", SupportsEffort: true, SupportedEffortLevels: []string{"ludicrous"}},
	}, nil))["probe"]
	require.NotNil(t, m)
	assert.Empty(t, m.SupportedEfforts)
}

// TestConvertClaudeModels_SentinelOwnsReservedDefaultID verifies the account-default
// sentinel is identified by its RAW value ("default") and OWNS the reserved "default"
// id. Launch (buildModelEffortArgs) and the badge logic (defaultModelForList) treat
// id=="default" as the sentinel, so a concrete model whose value merely normalizes to
// "default" can't share that id: it is dropped deterministically rather than
// masquerading as the sentinel (S3).
func TestConvertClaudeModels_SentinelOwnsReservedDefaultID(t *testing.T) {
	xhigh := []string{"low", "medium", "high", "xhigh", "max"}

	// "default5" normalizes to "default" (leading alphabetic token) but its raw value
	// is not the sentinel. It cannot claim the reserved "default" id, so it is dropped
	// entirely rather than surfaced under that id.
	require.Equal(t, "default", normalizeClaudeCodeModel("default5"), "precondition")
	require.NotContains(t,
		claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
			{Value: "default5", DisplayName: "Default Five", SupportsEffort: true, SupportedEffortLevels: xhigh},
		}, nil)),
		"default", "a concrete model normalizing to 'default' is dropped, not surfaced")

	// The real sentinel (raw value exactly "default") is surfaced and stripped of
	// efforts/window.
	sentinel := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "default", DisplayName: "Default", SupportsEffort: true, SupportedEffortLevels: xhigh},
	}, nil))["default"]
	require.NotNil(t, sentinel)
	assert.Empty(t, sentinel.SupportedEfforts, "the raw 'default' sentinel carries no effort menu")
	assert.Equal(t, int64(0), sentinel.ContextWindow)

	// Detection is case-insensitive (isDefaultSentinel uses EqualFold), so a
	// "Default" spelling is still treated as the sentinel and stripped.
	cased := claudeModelsByID(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "Default", DisplayName: "Default", SupportsEffort: true, SupportedEffortLevels: xhigh},
	}, nil))["default"]
	require.NotNil(t, cased)
	assert.Empty(t, cased.SupportedEfforts, "a 'Default' spelling is still the sentinel")
	assert.Equal(t, int64(0), cased.ContextWindow)

	// S3 determinism: when the CLI reports BOTH the sentinel and a concrete model
	// normalizing to "default", the sentinel always wins and the concrete is dropped,
	// regardless of their order -- so the catalog is the same either way.
	for _, order := range [][]claudeCodeModelInfo{
		{{Value: "default", DisplayName: "Default"}, {Value: "default5", DisplayName: "Default Five", SupportsEffort: true, SupportedEffortLevels: xhigh}},
		{{Value: "default5", DisplayName: "Default Five", SupportsEffort: true, SupportedEffortLevels: xhigh}, {Value: "default", DisplayName: "Default"}},
	} {
		got := convertClaudeModels(order, nil)
		require.Len(t, got, 1, "the concrete model normalizing to 'default' is dropped")
		require.Equal(t, "default", got[0].Id)
		assert.Empty(t, got[0].SupportedEfforts, "the sentinel (not the concrete model) owns the 'default' id")
	}
}

// TestConvertClaudeModels_ReproducesStaticCatalog locks the invariant that a CLI
// payload describing the current models converts to the same catalog the static
// fallback hardcodes (modulo IsDefault, which the manager applies). A no-effort
// model carries DefaultEffort "" in both -- the static catalog matches the
// conversion -- so this is compared unconditionally. If the static catalog and the
// conversion ever drift, this fails.
func TestConvertClaudeModels_ReproducesStaticCatalog(t *testing.T) {
	xhighLevels := []string{"low", "medium", "high", "xhigh", "max"}
	maxLevels := []string{"low", "medium", "high", "max"}
	payload := []claudeCodeModelInfo{
		// The live CLI reports Fable fully-qualified; it canonicalizes to the static
		// catalog's "fable[1m]" id.
		{Value: "claude-fable-5[1m]", DisplayName: "Fable 5", Description: "Most powerful for the hardest problems", SupportsEffort: true, SupportedEffortLevels: xhighLevels},
		{Value: "opus", DisplayName: "Opus", Description: "Most capable for complex work", SupportsEffort: true, SupportedEffortLevels: xhighLevels},
		{Value: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", SupportsEffort: true, SupportedEffortLevels: xhighLevels},
		{Value: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", SupportsEffort: true, SupportedEffortLevels: maxLevels},
		{Value: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks", SupportsEffort: true, SupportedEffortLevels: maxLevels},
		{Value: "haiku", DisplayName: "Haiku", Description: "Fastest for quick answers", SupportsEffort: false},
	}

	got := claudeModelsByID(convertClaudeModels(payload, nil))
	for _, want := range claudeCodeAvailableModels {
		// The "default" sentinel is a hand-authored placeholder (no concrete
		// model behind it), not a convert output, so it has no equivalent here.
		if want.Id == DefaultModelSentinel {
			continue
		}
		m := got[want.Id]
		require.NotNil(t, m, "converted catalog missing %q", want.Id)
		assert.Equal(t, want.DisplayName, m.DisplayName, "%q displayName", want.Id)
		assert.Equal(t, want.Description, m.Description, "%q description", want.Id)
		assert.Equal(t, want.ContextWindow, m.ContextWindow, "%q contextWindow", want.Id)
		assert.Equal(t, claudeEffortIDs(want), claudeEffortIDs(m), "%q efforts", want.Id)
		assert.False(t, m.IsDefault, "%q: convert never marks default", want.Id)
		assert.Equal(t, want.DefaultEffort, m.DefaultEffort, "%q defaultEffort", want.Id)
	}
}

func TestClaudeAvailableModels_DynamicFirstStaticFallback(t *testing.T) {
	dynamic := []*ModelInfo{
		{Id: "mythos", DisplayName: "Mythos 6"},
		{Id: "opus", DisplayName: "Opus"},
	}

	// Dynamic catalog present ⇒ returned verbatim.
	a := &ClaudeCodeAgent{availableModels: dynamic}
	assert.Equal(t, dynamic, a.availableModelCatalog())
	assert.Equal(t, dynamic, a.effortCatalog())

	// No dynamic catalog ⇒ static fallback.
	empty := &ClaudeCodeAgent{}
	assert.Equal(t, claudeCodeAvailableModels, empty.availableModelCatalog())
	assert.Equal(t, claudeCodeAvailableModels, empty.effortCatalog())

	// Third-party provider ⇒ availableModelCatalog hides everything, but effortCatalog
	// (the ungated picker backing) still returns the dynamic list -- availableModelCatalog
	// owns the third-party gate, not effortCatalog.
	thirdParty := &ClaudeCodeAgent{thirdPartyFromSettings: true, availableModels: dynamic}
	assert.Nil(t, thirdParty.availableModelCatalog())
	assert.Equal(t, dynamic, thirdParty.effortCatalog())
}

// TestBuildStartupFlagSettings_DynamicCatalogUltracode proves the end-to-end
// "auto from xhigh" path: a model the static catalog never heard of, discovered
// from the live CLI with xhigh support, gets ultracode enabled at startup.
func TestBuildStartupFlagSettings_DynamicCatalogUltracode(t *testing.T) {
	dynamic := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "mythos", DisplayName: "Mythos 6", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	}, nil)

	a := &ClaudeCodeAgent{model: "mythos", effort: EffortUltracode, availableModels: dynamic}
	fs := a.buildStartupFlagSettings(nil)
	assert.Equal(t, "xhigh", fs["effortLevel"])
	assert.Equal(t, true, fs["ultracode"])
}

// TestBuildStartupFlagSettings_DowngradesWhenDynamicDropsXHigh covers the other
// direction of the launch(static)-vs-startup(dynamic) catalog split: a model the
// static catalog launched at --effort xhigh (because static lists it as
// ultracode/xhigh-capable) but the LIVE CLI reports WITHOUT xhigh. Startup must
// correct the session DOWN to a level the dynamic catalog allows, not leave it
// stranded at an xhigh the CLI rejects.
func TestBuildStartupFlagSettings_DowngradesWhenDynamicDropsXHigh(t *testing.T) {
	// Live CLI reports opus without xhigh -- so dynamically it is not
	// ultracode-capable, even though the static catalog says it is.
	dynamic := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "opus", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
	}, nil)

	a := &ClaudeCodeAgent{model: "opus", effort: EffortUltracode, availableModels: dynamic}
	fs := a.buildStartupFlagSettings(nil)
	// Launch sent --effort xhigh (static opus is ultracode-capable); the dynamic
	// catalog resolves ultracode->high and clears the ultracode boolean.
	assert.Equal(t, "high", fs["effortLevel"])
	assert.Equal(t, false, fs["ultracode"])
}

// TestBuildStartupFlagSettings_SentinelEmitsNoEffort guards the S1+S3 interaction:
// the account-default sentinel launches without --model/--effort, and startup must
// not emit any effort flags (the resolved model's own default effort stands), even
// if a concrete effort was somehow stored alongside the sentinel.
func TestBuildStartupFlagSettings_SentinelEmitsNoEffort(t *testing.T) {
	a := &ClaudeCodeAgent{model: DefaultModelSentinel, effort: EffortUltracode}
	fs := a.buildStartupFlagSettings(nil)
	_, hasLevel := fs["effortLevel"]
	_, hasUltra := fs["ultracode"]
	assert.False(t, hasLevel, "sentinel must not emit effortLevel at startup")
	assert.False(t, hasUltra, "sentinel must not emit ultracode at startup")
}

// TestBuildStartupFlagSettings_FilteredModelHonorsLaunchVerdict covers the
// startup reconcile for a running model the dynamic catalog doesn't list (e.g. the
// CLI reported it in unavailable_models, so convertClaudeModels filtered it). We
// cannot reconcile against capabilities we don't have, so we honor what LAUNCH
// committed to instead -- resolved over the static catalog, the authority launch
// actually used:
//   - An ultracode launch of a static-ultracode model (opus/fable) DEFERRED the
//     ultracode boolean to this step (launch sent only --effort xhigh; the boolean
//     is applied nowhere else), so we complete the combo rather than silently
//     running the model the user picked ultracode for at plain xhigh.
//   - A non-ultracode launch (sonnet+max) and a model unknown to BOTH catalogs are
//     left exactly as launched -- no downgrade, no spurious flags.
func TestBuildStartupFlagSettings_FilteredModelHonorsLaunchVerdict(t *testing.T) {
	// Dynamic catalog lists only sonnet, so opus/fable/unknown are all "filtered".
	dynamic := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
	}, nil)

	// opus+ultracode: static knows opus is ultracode-capable and launch deferred the
	// boolean here, so completing the combo preserves the user's choice.
	opus := &ClaudeCodeAgent{model: "opus", effort: EffortUltracode, availableModels: dynamic}
	fs := opus.buildStartupFlagSettings(nil)
	assert.Equal(t, EffortXHigh, fs["effortLevel"], "filtered ultracode model completes the combo (xhigh base)")
	assert.Equal(t, true, fs["ultracode"], "filtered static-ultracode model keeps its ultracode boolean")

	// sonnet+max: launch sent --effort max (no deferred boolean); a filtered model is
	// left as launched, never downgraded.
	sonnet := &ClaudeCodeAgent{model: "sonnet", effort: "max", availableModels: dynamic}
	fs = sonnet.buildStartupFlagSettings(nil)
	_, hasLevel := fs["effortLevel"]
	_, hasUltra := fs["ultracode"]
	assert.False(t, hasLevel, "a filtered non-ultracode model is not downgraded")
	assert.False(t, hasUltra, "a filtered non-ultracode model emits no ultracode flag")

	// claude-future-preview+ultracode: unknown to BOTH catalogs. Launch (static)
	// resolved ultracode->high for an untrusted model, so it never ran ultracode;
	// startup must not invent it.
	unknown := &ClaudeCodeAgent{model: "claude-future-preview", effort: EffortUltracode, availableModels: dynamic}
	fs = unknown.buildStartupFlagSettings(nil)
	_, hasLevel = fs["effortLevel"]
	_, hasUltra = fs["ultracode"]
	assert.False(t, hasLevel, "a model unknown to both catalogs gets no startup effort flags")
	assert.False(t, hasUltra, "a model unknown to both catalogs gets no ultracode flag")
}

// TestBuildStartupFlagSettings_ThirdPartyEmitsNoEffort covers S2: a third-party-LLM
// session presents no model/effort UI (AvailableModels returns nil) and must not be
// pushed any effort/ultracode flags at startup -- even when its stored model+effort
// would otherwise resolve to the ultracode combo -- since its user can neither see nor
// control effort. Gated on the same hidesModelEffortUI predicate AvailableModels uses.
func TestBuildStartupFlagSettings_ThirdPartyEmitsNoEffort(t *testing.T) {
	dynamic := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "opus", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	}, nil)

	a := &ClaudeCodeAgent{model: "opus", effort: EffortUltracode, availableModels: dynamic, thirdPartyFromSettings: true}
	fs := a.buildStartupFlagSettings(nil)
	_, hasLevel := fs["effortLevel"]
	_, hasUltra := fs["ultracode"]
	assert.False(t, hasLevel, "third-party session emits no startup effortLevel")
	assert.False(t, hasUltra, "third-party session emits no startup ultracode flag")

	// Control: the identical agent WITHOUT the third-party flag DOES complete the combo,
	// proving the suppression is the third-party gate, not an unrelated no-op.
	a.thirdPartyFromSettings = false
	fs = a.buildStartupFlagSettings(nil)
	assert.Equal(t, true, fs["ultracode"], "a non-third-party session emits the ultracode combo")
}

// TestBuildStartupFlagSettings_AppliesEffortWhenDynamicAddsEfforts covers S9: a model
// the STATIC catalog treats as effort-less (so launch omitted --effort) but the live
// CLI reports WITH efforts. Startup must apply the stored effort against the dynamic
// catalog so the live selector and the running session agree, instead of leaving the
// session at the CLI's own default while the UI shows a concrete level.
func TestBuildStartupFlagSettings_AppliesEffortWhenDynamicAddsEfforts(t *testing.T) {
	// Haiku is effort-less in the static catalog, but here the live CLI reports it
	// with an effort menu (the hypothetical static/dynamic disagreement S9 guards).
	dynamic := convertClaudeModels([]claudeCodeModelInfo{
		{Value: "haiku", DisplayName: "Haiku", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high"}},
	}, nil)
	a := &ClaudeCodeAgent{model: "haiku", effort: EffortHigh, availableModels: dynamic}
	fs := a.buildStartupFlagSettings(nil)
	assert.Equal(t, EffortHigh, fs["effortLevel"], "launch omitted --effort (static effort-less); startup applies the dynamic level")
	_, hasUltra := fs["ultracode"]
	assert.False(t, hasUltra, "no ultracode for a non-xhigh model")

	// EffortAuto on the same model still keeps the CLI default (nothing to apply).
	autoAgent := &ClaudeCodeAgent{model: "haiku", effort: EffortAuto, availableModels: dynamic}
	_, hasLevel := autoAgent.buildStartupFlagSettings(nil)["effortLevel"]
	assert.False(t, hasLevel, "auto effort keeps the CLI default even when dynamic offers efforts")
}

// TestWithDefaultModelMarked_ClaudeDefaultSentinel verifies the default badge
// tracks the account: the CLI's "default" sentinel entry is marked when present,
// an operator env override wins over it, and a list without the sentinel falls
// back to the configured default.
func TestWithDefaultModelMarked_ClaudeDefaultSentinel(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	// Account-specific list (no opus[1m]) that includes the CLI "default"
	// sentinel: the sentinel carries the IsDefault badge.
	withSentinel := []*ModelInfo{
		{Id: "default", DisplayName: "Default (recommended)"},
		{Id: "claude-fable-5[1m]", DisplayName: "Fable"},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}
	assert.Equal(t, "default", markedModelID(withDefaultModelMarked(withSentinel, claude)))

	// The static catalog carries the sentinel, so it is marked there too.
	assert.Equal(t, "default", DefaultModel(claude))
	assert.Equal(t, "default", markedModelID(withDefaultModelMarked(claudeCodeAvailableModels, claude)))

	// A list missing the sentinel (a CLI that doesn't report it) falls back to
	// the highest-preference entry present, so the picker still shows a default
	// badge -- the configured default ("default") isn't in the list to mark.
	noSentinel := []*ModelInfo{
		{Id: "opus[1m]", DisplayName: "Opus (1M context)"},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}
	assert.Equal(t, "opus[1m]", markedModelID(withDefaultModelMarked(noSentinel, claude)))

	// Operator override wins over the CLI sentinel.
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "sonnet")
	assert.Equal(t, "sonnet", markedModelID(withDefaultModelMarked(withSentinel, claude)))
}

// TestUpdateSettings_SwitchToDefaultRestarts verifies that switching to the
// account-default sentinel signals a restart (returns false) rather than
// pushing an unresolvable "default" model through apply_flag_settings.
func TestUpdateSettings_SwitchToDefaultRestarts(t *testing.T) {
	a := &ClaudeCodeAgent{model: "claude-fable-5[1m]", effort: EffortHigh}
	assert.False(t, a.UpdateSettings(map[string]string{OptionIDModel: DefaultModelSentinel}))
}

// TestClaudeOptionGroups_HiddenModelEffortUISurfacesReadOnly verifies a third-party
// (hidden model/effort UI) session still surfaces its concrete model -- and a concrete
// effort -- as READ-ONLY groups, so `remote agent get`/list and the UI show what's
// running instead of a blank.
func TestClaudeOptionGroups_HiddenModelEffortUISurfacesReadOnly(t *testing.T) {
	a := &ClaudeCodeAgent{
		thirdPartyFromSettings: true,
		model:                  "third-party-model",
		effort:                 EffortHigh,
	}

	groups := a.OptionGroups()

	mg := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, mg, "the model is surfaced even with hidden model/effort UI")
	assert.False(t, mg.GetMutable(), "but read-only -- the model is not user-changeable here")
	assert.Equal(t, "third-party-model", mg.GetCurrentValue())

	eg := optionids.GroupByID(groups, OptionIDEffort)
	require.NotNil(t, eg, "a concrete effort is surfaced read-only too")
	assert.False(t, eg.GetMutable())
	assert.Equal(t, EffortHigh, eg.GetCurrentValue())
}

// An auto/empty effort is not surfaced for a hidden-UI session (nothing meaningful to show).
func TestClaudeOptionGroups_HiddenUIAutoEffortOmitsEffortGroup(t *testing.T) {
	a := &ClaudeCodeAgent{thirdPartyFromSettings: true, model: "tp", effort: EffortAuto}

	groups := a.OptionGroups()

	assert.NotNil(t, optionids.GroupByID(groups, OptionIDModel))
	assert.Nil(t, optionids.GroupByID(groups, OptionIDEffort), "auto effort is not surfaced")
}
