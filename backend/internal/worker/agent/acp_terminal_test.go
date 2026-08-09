//go:build unix

package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestBuildACPTerminalCmd_ShellVsArgv(t *testing.T) {
	cmd := buildACPTerminalCmd(t.Context(), "echo hi", nil)
	require.NotNil(t, cmd)
	assert.Contains(t, cmd.Path, "sh")
	assert.Equal(t, []string{"-c", "echo hi"}, cmd.Args[1:])

	cmd = buildACPTerminalCmd(t.Context(), "echo", []string{"hi"})
	require.NotNil(t, cmd)
	assert.Equal(t, []string{"hi"}, cmd.Args[1:])
}

// responseRecorder captures JSON-RPC responses written to an agent's stdin.
type responseRecorder struct {
	mu   sync.Mutex
	bufs [][]byte
	ch   chan struct{}
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.bufs = append(r.bufs, append([]byte(nil), p...))
	r.mu.Unlock()
	select {
	case r.ch <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (r *responseRecorder) Close() error { return nil }

func (r *responseRecorder) wait(t *testing.T, n int, timeout time.Duration) []map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		got := len(r.bufs)
		r.mu.Unlock()
		if got >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d responses (got %d)", n, got)
		}
		select {
		case <-r.ch:
		case <-time.After(20 * time.Millisecond):
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(r.bufs))
	for _, b := range r.bufs {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(bytes.TrimSpace(b), &m))
		out = append(out, m)
	}
	return out
}

func newTerminalTestBase(t *testing.T, sink *testSink) (*acpBase, *responseRecorder) {
	t.Helper()
	rec := &responseRecorder{ch: make(chan struct{}, 8)}
	b := &acpBase{
		jsonrpcBase: jsonrpcBase{
			processBase: processBase{
				agentID:      "agent-1",
				providerName: "goose",
				stdin:        rec,
			},
		},
		sink:       sink,
		sessionID:  "sess-1",
		workingDir: t.TempDir(),
	}
	return b, rec
}

func dispatchTerminal(b *acpBase, method string, rpcID int, params any) {
	rawParams, _ := json.Marshal(params)
	line, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"method":  method,
		"params":  json.RawMessage(rawParams),
	})
	b.handleACPOutput(parseLine(line), nil, nil)
}

func TestACPTerminal_CreateWaitOutputRelease(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf 'hello-acp'",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	require.Nil(t, resps[0]["error"])
	result := resps[0]["result"].(map[string]interface{})
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	sink.bgTasksMu.Lock()
	row, ok := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "printf 'hello-acp'", row.Title)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2, 5*time.Second)
	waitResult := resps[1]["result"].(map[string]interface{})
	assert.EqualValues(t, 0, waitResult["exitCode"])

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 3*time.Second)
	outResult := resps[2]["result"].(map[string]interface{})
	assert.Contains(t, outResult["output"], "hello-acp")
	assert.Equal(t, false, outResult["truncated"])
	exitStatus, ok := outResult["exitStatus"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 0, exitStatus["exitCode"])

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)

	sink.bgTasksMu.Lock()
	row = sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.Equal(t, bgtask.StatusCompleted, row.Status)

	b.terminalsMu.Lock()
	_, still := b.terminals[termID]
	b.terminalsMu.Unlock()
	assert.False(t, still)
}

func TestACPTerminal_KillThenWait(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalKill, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2, 3*time.Second)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 5*time.Second)
	waitResult := resps[2]["result"].(map[string]interface{})
	// Killed process reports a signal (or non-zero) rather than success.
	if waitResult["exitCode"] != nil {
		assert.NotEqualValues(t, 0, waitResult["exitCode"])
	} else {
		assert.NotNil(t, waitResult["signal"])
	}

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)

	sink.bgTasksMu.Lock()
	row := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestACPTerminal_OutputByteLimitTruncates(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	limit := 8

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "printf 'abcdefghijklmnop'",
		"cwd":             b.workingDir,
		"outputByteLimit": limit,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2, 5*time.Second)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 3*time.Second)
	outResult := resps[2]["result"].(map[string]interface{})
	assert.Equal(t, true, outResult["truncated"])
	out := outResult["output"].(string)
	assert.LessOrEqual(t, len(out), limit)
	assert.True(t, len(out) > 0)

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}

func TestACPTerminal_UnknownTerminalID(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalOutput, 1, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_missing",
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
}

func TestACPTerminal_RelativeCwdRejected(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "echo hi",
		"cwd":       "relative/path",
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
}

func TestACPTerminal_ReleaseAllOnStop(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	// Stop closes stdin; give it a writer that swallows the close.
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.stdin = struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw}
	b.processDone = make(chan struct{})
	close(b.processDone) // Stop's grace wait sees a finished "process"

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	b.Stop()

	b.terminalsMu.Lock()
	assert.Empty(t, b.terminals)
	b.terminalsMu.Unlock()

	sink.bgTasksMu.Lock()
	row := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.True(t, row.Status.IsTerminal())
}

func TestACPTerminal_DefaultCwdFromWorkingDir(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	marker := filepath.Join(b.workingDir, "marker.txt")
	require.NoError(t, os.WriteFile(marker, []byte("x"), 0o644))

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "test -f marker.txt && echo found",
		// cwd omitted — must use workingDir
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2, 5*time.Second)
	assert.EqualValues(t, 0, resps[1]["result"].(map[string]interface{})["exitCode"])

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 3*time.Second)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "found")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}

func TestACPTerminal_EmptyCommandRejected(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "command is required")
}

func TestACPTerminal_SessionIDMismatch(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "other-session",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "sessionId mismatch")
}

func TestACPTerminal_NoActiveSession(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	b.sessionID = ""

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "no active session")
}

func TestACPTerminal_NegativeOutputByteLimitRejected(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "echo hi",
		"cwd":             b.workingDir,
		"outputByteLimit": -1,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "outputByteLimit")
}

func TestACPTerminal_ArgvStyleCommand(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf",
		"args":      []string{"argv-ok"},
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	require.Nil(t, resps[0]["error"])
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2, 5*time.Second)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 3*time.Second)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "argv-ok")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}

func TestACPTerminal_EnvOverridesApplied(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf '%s' \"$LEAPMUX_ACP_TERM_TEST\"",
		"cwd":       b.workingDir,
		"env": []map[string]string{
			{"name": "LEAPMUX_ACP_TERM_TEST", "value": "from-host"},
		},
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2, 5*time.Second)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 3*time.Second)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "from-host")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}

func TestACPTerminal_NonZeroExitMarksFailed(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "exit 7",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2, 5*time.Second)
	assert.EqualValues(t, 7, resps[1]["result"].(map[string]interface{})["exitCode"])

	sink.bgTasksMu.Lock()
	row := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.Equal(t, bgtask.StatusFailed, row.Status)

	dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 3, 3*time.Second)
}

func TestACPTerminal_OutputBeforeExitOmitsExitStatus(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalOutput, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2, 3*time.Second)
	outResult := resps[1]["result"].(map[string]interface{})
	_, hasExit := outResult["exitStatus"]
	assert.False(t, hasExit, "in-flight terminals must omit exitStatus")

	dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 3, 5*time.Second)
}

func TestACPTerminal_WaitForExitDoesNotBlockCaller(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 1",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	// Dispatch kill immediately after wait_for_exit to prove the read-loop
	// handler returned without waiting for the sleep to finish.
	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	dispatchTerminal(b, acpMethodTerminalKill, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3, 5*time.Second)
	ids := make([]float64, 0, 2)
	for _, r := range resps[1:] {
		ids = append(ids, r["id"].(float64))
	}
	assert.Contains(t, ids, float64(3), "kill must be handled while wait_for_exit is outstanding")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 5*time.Second)
}

func TestACPTerminal_KillAndReleaseUnknownID(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalKill, 1, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_gone",
	})
	dispatchTerminal(b, acpMethodTerminalRelease, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_gone",
	})
	resps := rec.wait(t, 2, 3*time.Second)
	assert.EqualValues(t, -32602, resps[0]["error"].(map[string]interface{})["code"])
	assert.EqualValues(t, -32602, resps[1]["error"].(map[string]interface{})["code"])
}

func TestACPTerminal_MalformedParams(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	line, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  acpMethodTerminalCreate,
		"params":  "not-an-object",
	})
	b.handleACPOutput(parseLine(line), nil, nil)
	resps := rec.wait(t, 1, 3*time.Second)
	assert.EqualValues(t, -32602, resps[0]["error"].(map[string]interface{})["code"])
}

func TestACPTerminal_MissingJSONRPCIDIsIgnored(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	line, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  acpMethodTerminalCreate,
		"params": map[string]interface{}{
			"sessionId": "sess-1",
			"command":   "echo hi",
			"cwd":       b.workingDir,
		},
	})
	b.handleACPOutput(parseLine(line), nil, nil)

	select {
	case <-rec.ch:
		t.Fatal("expected no JSON-RPC response when id is missing")
	case <-time.After(100 * time.Millisecond):
	}
	b.terminalsMu.Lock()
	assert.Empty(t, b.terminals)
	b.terminalsMu.Unlock()
}
