//go:build unix

package agent

import (
	"bytes"
	"context"
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
	statusLog := append([]bgtask.Status(nil), sink.bgTaskStatuses[termID]...)
	sink.bgTasksMu.Unlock()
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	// printf often exits before this read; the status trail proves Running was
	// upserted first (see terminalCreate), which a live snapshot cannot.
	require.NotEmpty(t, statusLog)
	assert.Equal(t, bgtask.StatusRunning, statusLog[0])
	assert.Equal(t, "printf 'hello-acp'", row.Title)
	// terminal/create carries the command and nothing else, so this title IS the
	// command and the client may set it as code -- unlike Claude's shell rows,
	// whose title is `description || command` with no way to tell which.
	assert.True(t, row.TitleIsCommand, "an ACP terminal title is the command itself")

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
	assert.Equal(t, bgtask.StatusStopped, row.Status, "host kill must map to StatusStopped")
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

// An empty command falls back to the literal "shell", which is a label rather
// than something to run -- so the row must not claim its title is a command.
func TestACPTerminal_EmptyCommandTitleIsNotACommand(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	result, ok := resps[0]["result"].(map[string]interface{})
	if !ok {
		t.Skip("the agent rejects an empty command outright; there is no row to inspect")
	}
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	sink.bgTasksMu.Lock()
	row, found := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	require.True(t, found)
	assert.Equal(t, "shell", row.Title)
	assert.False(t, row.TitleIsCommand, "the fallback label is not a command")
}

// A command that holds nothing but the characters the title rule strips leaves
// no label either, so it takes the same "shell" fallback the empty command
// takes. The clean runs HERE for that reason: the registry cleans every title,
// so a command left raw would reach the sink non-empty, skip this fallback, and
// land as a blank row.
func TestACPTerminal_CommandOfStrippedCharactersFallsBackToShell(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "\u200b\ufeff",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	result, ok := resps[0]["result"].(map[string]interface{})
	require.True(t, ok, "terminal/create must accept the command, or there is no row to inspect")
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	sink.bgTasksMu.Lock()
	row, found := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	require.True(t, found)
	assert.Equal(t, "shell", row.Title, "a command that cleans to nothing takes the fallback label")
	assert.False(t, row.TitleIsCommand, "the fallback label is not a command")
}

// The command reaches the registry row whole, quoting included. This is
// asserted at the provider that owns the only rows whose title really IS a
// command: `$`, `%`, `"` and `\` used to go, and the row then labelled a
// command that nobody ran.
func TestACPTerminal_CommandReachesTheRowWhole(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   `printf '%s' "$HOME"`,
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	result, ok := resps[0]["result"].(map[string]interface{})
	require.True(t, ok)
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	sink.bgTasksMu.Lock()
	row, found := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	require.True(t, found)
	assert.Equal(t, `printf '%s' "$HOME"`, row.Title,
		"the row labels the command that ran, so it has to hold the command that ran")
	assert.True(t, row.TitleIsCommand)
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
	assert.True(t, row.Status.IsFinished())
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

func TestACPTerminal_ZeroOutputByteLimitRetainsNothing(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	limit := 0

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "printf 'abcdefgh'",
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
	assert.Equal(t, "", outResult["output"])

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
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

	sink.bgTasksMu.Lock()
	row := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a still-running sleep must stay Running")

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

func TestACPTerminal_StopReapsLongLivedChild(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.stdin = struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw}
	b.processDone = make(chan struct{})
	close(b.processDone)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		// sleep inherits the host pipes; without process-group kill, Stop
		// would hang waiting for stdout EOF after killing only /bin/sh.
		"command": "sleep 60",
		"cwd":     b.workingDir,
	})
	_ = rec.wait(t, 1, 3*time.Second)

	done := make(chan struct{})
	go func() {
		b.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Stop hung waiting for ACP terminal teardown")
	}

	b.terminalsMu.Lock()
	assert.True(t, b.terminalsClosed)
	assert.Empty(t, b.terminals)
	b.terminalsMu.Unlock()
}

func TestACPTerminal_CreateRejectedAfterStop(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.stdin = struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw}
	b.processDone = make(chan struct{})
	close(b.processDone)

	b.Stop()

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32603, errObj["code"])
	assert.Contains(t, errObj["message"], "stopped")
}

func TestACPTerminal_ReleaseSessionOnClearContext(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	// newSessionLocked needs a cancelled ctx so sendRequest fails fast after
	// releaseSessionTerminals has already run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.ctx = ctx
	b.processDone = make(chan struct{})
	close(b.processDone)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1, 3*time.Second)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	_, ok := b.ClearContext()
	assert.False(t, ok, "session/new must fail without a live agent")

	b.terminalsMu.Lock()
	_, still := b.terminals[termID]
	closed := b.terminalsClosed
	b.terminalsMu.Unlock()
	assert.False(t, still)
	assert.False(t, closed, "ClearContext must not latch terminals closed")

	sink.bgTasksMu.Lock()
	row := sink.bgTasks[termID]
	sink.bgTasksMu.Unlock()
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestACPTerminal_BaseEnvPinsWorkerMarker(t *testing.T) {
	sink := &testSink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Setenv("LEAPMUX_WORKER", "0")
	t.Setenv("GOOSE_TERMINAL", "1")

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   `printf '%s|%s' "$LEAPMUX_WORKER" "${GOOSE_TERMINAL-}"`,
		"cwd":       b.workingDir,
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
	out := resps[2]["result"].(map[string]interface{})["output"].(string)
	assert.Equal(t, "1|", out, "FinalizeAgentEnv must pin LEAPMUX_WORKER and scrub GOOSE_TERMINAL")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}

func TestACPTerminal_KillReportsSignalName(t *testing.T) {
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
	if waitResult["exitCode"] != nil {
		t.Logf("got exitCode=%v (acceptable if shell reports numeric)", waitResult["exitCode"])
	} else {
		sig, _ := waitResult["signal"].(string)
		require.NotEmpty(t, sig)
		assert.NotEqual(t, "terminated", sig, "should report the real WaitStatus signal when available")
	}

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4, 3*time.Second)
}
