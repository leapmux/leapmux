//go:build unix

package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

// responseWaitDeadline limits a wait for replies that must arrive. A wait
// returns as soon as the replies arrive, so the generous limit costs a passing
// test nothing. A short limit fails a correct run on a loaded machine.
const responseWaitDeadline = 30 * time.Second

// wait returns the first n replies, and fails the test when they do not arrive
// within responseWaitDeadline.
func (r *responseRecorder) wait(t *testing.T, n int) []map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(responseWaitDeadline)
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

func newTerminalTestBase(t *testing.T, sink *agenttest.Sink) (*Base, *responseRecorder) {
	t.Helper()
	rec := &responseRecorder{ch: make(chan struct{}, 8)}
	b := &Base{
		JSONRPCProcess: providerkit.JSONRPCProcess{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID:      "agent-1",
				ProviderName: "goose",
				Stdin:        rec,
			}),
		},
		sink:       agent.NewProviderServices(sink),
		sessionID:  "sess-1",
		workingDir: t.TempDir(),
	}
	b.bind(b)
	return b, rec
}

func dispatchTerminal(b *Base, method string, rpcID int, params any) {
	rawParams, _ := json.Marshal(params)
	line, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"method":  method,
		"params":  json.RawMessage(rawParams),
	})
	b.handleACPOutput(providerkit.ParseLine(line))
}

func TestACPTerminal_CreateWaitOutputRelease(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf 'hello-acp'",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	require.Nil(t, resps[0]["error"])
	result := resps[0]["result"].(map[string]interface{})
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	row, ok := sink.BackgroundTask(termID)
	statusLog := sink.BackgroundTaskStatuses(termID)
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	// printf often exits before this read; the status trail proves Running was
	// upserted first (see terminalCreate), which a live snapshot cannot.
	require.NotEmpty(t, statusLog)
	assert.Equal(t, bgtask.StatusRunning, statusLog[0])
	assert.Contains(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, row.Status,
		"create must leave the row Running or already Completed, not Failed/Stopped")
	assert.Equal(t, "printf 'hello-acp'", row.Title)
	// terminal/create carries the command and nothing else, so this title IS the
	// command and the client may set it as code -- unlike Claude's shell rows,
	// whose title is `description || command` with no way to tell which.
	assert.True(t, row.TitleIsCommand, "an ACP terminal title is the command itself")

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2)
	waitResult := resps[1]["result"].(map[string]interface{})
	assert.EqualValues(t, 0, waitResult["exitCode"])

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
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
	_ = rec.wait(t, 4)

	row, _ = sink.BackgroundTask(termID)
	statusLog = sink.BackgroundTaskStatuses(termID)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, statusLog)

	b.terminalsMu.Lock()
	_, still := b.terminals[termID]
	b.terminalsMu.Unlock()
	assert.False(t, still)
}

func TestACPTerminal_KillThenWait(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalKill, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
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
	_ = rec.wait(t, 4)

	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusStopped, row.Status, "host kill must map to StatusStopped")
}

func TestACPTerminal_OutputByteLimitTruncates(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	limit := 8

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "printf 'abcdefghijklmnop'",
		"cwd":             b.workingDir,
		"outputByteLimit": limit,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("terminal:"+termID))

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	outResult := resps[2]["result"].(map[string]interface{})
	assert.Equal(t, true, outResult["truncated"])
	out := outResult["output"].(string)
	assert.LessOrEqual(t, len(out), limit)
	assert.True(t, len(out) > 0)

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_ClampsRequestedOutputLimit(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	hugeLimit := acpMaxOutputByteLimit * 1024
	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "printf x",
		"cwd":             b.workingDir,
		"outputByteLimit": hugeLimit,
	})
	responses := rec.wait(t, 1)
	termID := responses[0]["result"].(map[string]interface{})["terminalId"].(string)
	session, ok := b.getTerminal(termID)
	require.True(t, ok)
	assert.Equal(t, acpMaxOutputByteLimit, session.byteLimit)
	b.releaseAllTerminals()
}

func TestACPTerminal_UnknownTerminalID(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalOutput, 1, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_missing",
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
}

// An empty command falls back to the literal "shell", which is a label rather
// than something to run -- so the row must not claim its title is a command.
func TestACPTerminal_EmptyCommandTitleIsNotACommand(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	result, ok := resps[0]["result"].(map[string]interface{})
	if !ok {
		t.Skip("the agent rejects an empty command outright; there is no row to inspect")
	}
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	row, found := sink.BackgroundTask(termID)
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
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "\u200b\ufeff",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	result, ok := resps[0]["result"].(map[string]interface{})
	require.True(t, ok, "terminal/create must accept the command, or there is no row to inspect")
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	row, found := sink.BackgroundTask(termID)
	require.True(t, found)
	assert.Equal(t, "shell", row.Title, "a command that cleans to nothing takes the fallback label")
	assert.False(t, row.TitleIsCommand, "the fallback label is not a command")
}

// The command reaches the registry row whole, quoting included. This is
// asserted at the provider that owns the only rows whose title really IS a
// command: `$`, `%`, `"` and `\` used to go, and the row then labelled a
// command that nobody ran.
func TestACPTerminal_CommandReachesTheRowWhole(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   `printf '%s' "$HOME"`,
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	result, ok := resps[0]["result"].(map[string]interface{})
	require.True(t, ok)
	termID, _ := result["terminalId"].(string)
	require.NotEmpty(t, termID)

	row, found := sink.BackgroundTask(termID)
	require.True(t, found)
	assert.Equal(t, `printf '%s' "$HOME"`, row.Title,
		"the row labels the command that ran, so it has to hold the command that ran")
	assert.True(t, row.TitleIsCommand)
}

func TestACPTerminal_RelativeCwdRejected(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "echo hi",
		"cwd":       "relative/path",
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
}

func TestACPTerminal_ReleaseAllOnStop(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	// Stop closes stdin; give it a writer that swallows the close.
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.SetStdinForTest(struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw})
	b.SimulateExitForTest() // Stop's grace wait sees a finished "process"

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	b.Stop()

	b.terminalsMu.Lock()
	assert.Empty(t, b.terminals)
	b.terminalsMu.Unlock()

	row, _ := sink.BackgroundTask(termID)
	assert.True(t, row.Status.IsFinished())
}

// A stop must end the command that the stopped tool still waits on.
//
// fast-agent 0.10.42 answers session/cancel with the `cancelled` stop reason, but
// it never kills or releases the terminal of its cancelled tool: its terminal
// runtime catches only Exception, and asyncio.CancelledError is a BaseException.
// The command ran on, its row stayed Running, the Worker kept the agent working,
// and the thinking indicator never cleared after the reader pressed Stop.
func TestACPTerminal_StoppedPromptReleasesTheTerminalItAwaits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response json.RawMessage
		err      error
	}{
		{name: "cancelled response", response: json.RawMessage(`{"stopReason":"cancelled"}`)},
		{name: "failed prompt", err: errors.New("context canceled")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			b, rec := newTerminalTestBase(t, sink)
			t.Cleanup(b.releaseAllTerminals)

			dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
				"sessionId": "sess-1",
				"command":   "sleep 30",
				"cwd":       b.workingDir,
			})
			resps := rec.wait(t, 1)
			termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)
			dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
				"sessionId":  "sess-1",
				"terminalId": termID,
			})
			b.Mu.Lock()
			b.promptActive = true
			b.Mu.Unlock()
			b.noteACPInterruptRequested()

			b.finishPromptRequest("sess-1", tc.response, tc.err)

			row, _ := sink.BackgroundTask(termID)
			assert.Equal(t, bgtask.StatusStopped, row.Status,
				"the reader stopped the turn, so the command that its tool awaited must stop too")
			b.terminalsMu.Lock()
			_, still := b.terminals[termID]
			b.terminalsMu.Unlock()
			assert.False(t, still, "nothing will release a terminal of a cancelled tool, so the host forgets it")
			resps = rec.wait(t, 2)
			waitResult := resps[1]["result"].(map[string]interface{})
			if waitResult["exitCode"] != nil {
				assert.NotEqualValues(t, 0, waitResult["exitCode"])
			} else {
				assert.NotNil(t, waitResult["signal"], "the pending wait still gets its answer: the kill")
			}
		})
	}
}

// createTerminalForTest starts command on b and returns its terminal ID. rpcID
// is the JSON-RPC ID of the create request, and the reply is the response at
// position replies-1 of rec.
func createTerminalForTest(t *testing.T, b *Base, rec *responseRecorder, rpcID, replies int, command string) string {
	t.Helper()
	dispatchTerminal(b, acpMethodTerminalCreate, rpcID, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   command,
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, replies)
	require.Nil(t, resps[replies-1]["error"])
	termID, _ := resps[replies-1]["result"].(map[string]interface{})["terminalId"].(string)
	require.NotEmpty(t, termID)
	return termID
}

// startStoppedPromptForTest marks a prompt active on b and records the reader's
// stop, as Interrupt does.
func startStoppedPromptForTest(b *Base) {
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.noteACPInterruptRequested()
}

func terminalHeldForTest(b *Base, termID string) bool {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	_, held := b.terminals[termID]
	return held
}

// A terminal that the agent does not wait on can be background work that it
// reads in a later turn, so a stop leaves it alone.
func TestACPTerminal_StoppedPromptKeepsATerminalThatNothingAwaits(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Cleanup(b.releaseAllTerminals)
	termID := createTerminalForTest(t, b, rec, 1, 1, "sleep 30")
	startStoppedPromptForTest(b)

	b.finishPromptRequest("sess-1", json.RawMessage(`{"stopReason":"cancelled"}`), nil)

	assert.True(t, terminalHeldForTest(b, termID))
	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

// Without a stop, a pending wait is the agent's own business: the turn ended,
// and the agent can still read the exit later.
func TestACPTerminal_PromptWithoutAStopKeepsTheTerminalItAwaits(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Cleanup(b.releaseAllTerminals)
	termID := createTerminalForTest(t, b, rec, 1, 1, "sleep 30")
	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.finishPromptRequest("sess-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.True(t, terminalHeldForTest(b, termID))
	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

// A wait that already got its reply no longer counts. The command exited, so a
// stop has nothing to end, and the agent can still read the output and release
// the terminal.
func TestACPTerminal_StoppedPromptKeepsAnExitedTerminal(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Cleanup(b.releaseAllTerminals)
	termID := createTerminalForTest(t, b, rec, 1, 1, "printf done")
	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps := rec.wait(t, 2)
	assert.EqualValues(t, 0, resps[1]["result"].(map[string]interface{})["exitCode"])
	startStoppedPromptForTest(b)

	b.finishPromptRequest("sess-1", json.RawMessage(`{"stopReason":"cancelled"}`), nil)

	assert.True(t, terminalHeldForTest(b, termID))
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, sink.BackgroundTaskStatuses(termID),
		"an exited command keeps its own outcome rather than a stop")
}

// Each pending wait gets its reply when the stop ends the command.
func TestACPTerminal_StoppedPromptAnswersEveryPendingWait(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Cleanup(b.releaseAllTerminals)
	first := createTerminalForTest(t, b, rec, 1, 1, "sleep 30")
	second := createTerminalForTest(t, b, rec, 2, 2, "sleep 30")
	for rpcID, termID := range map[int]string{3: first, 4: first, 5: second} {
		dispatchTerminal(b, acpMethodTerminalWaitForExit, rpcID, map[string]interface{}{
			"sessionId":  "sess-1",
			"terminalId": termID,
		})
	}
	startStoppedPromptForTest(b)

	b.finishPromptRequest("sess-1", json.RawMessage(`{"stopReason":"cancelled"}`), nil)

	resps := rec.wait(t, 5)
	answered := map[float64]bool{}
	for _, resp := range resps[2:] {
		id, _ := resp["id"].(float64)
		answered[id] = true
		result := resp["result"].(map[string]interface{})
		assert.True(t, result["signal"] != nil || (result["exitCode"] != nil && result["exitCode"] != float64(0)),
			"wait %v must report the kill, got %v", id, result)
	}
	assert.Equal(t, map[float64]bool{3: true, 4: true, 5: true}, answered)
	for _, termID := range []string{first, second} {
		assert.False(t, terminalHeldForTest(b, termID))
		assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusStopped}, sink.BackgroundTaskStatuses(termID))
	}
}

// The agent can release the same terminal while the stop releases it. Only one
// of the two may end it, so the row closes once.
func TestACPTerminal_StoppedPromptReleaseRacesTheAgentRelease(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Cleanup(b.releaseAllTerminals)
	termID := createTerminalForTest(t, b, rec, 1, 1, "sleep 30")
	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.releaseAwaitedTerminals()
	}()
	go func() {
		defer wg.Done()
		dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
			"sessionId":  "sess-1",
			"terminalId": termID,
		})
	}()
	wg.Wait()

	// The create reply, the wait reply, and the release reply: success when the
	// agent took the terminal first, "unknown terminalId" when the stop did.
	resps := rec.wait(t, 3)
	assert.Len(t, resps, 3)
	assert.False(t, terminalHeldForTest(b, termID))
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusStopped}, sink.BackgroundTaskStatuses(termID))
}

func TestACPTerminal_DefaultCwdFromWorkingDir(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	marker := filepath.Join(b.workingDir, "marker.txt")
	require.NoError(t, os.WriteFile(marker, []byte("x"), 0o644))

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "test -f marker.txt && echo found",
		// cwd omitted — must use workingDir
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2)
	assert.EqualValues(t, 0, resps[1]["result"].(map[string]interface{})["exitCode"])

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "found")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_EmptyCommandRejected(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "command is required")
}

func TestACPTerminal_SessionIDMismatch(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "other-session",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "sessionId mismatch")
}

func TestACPTerminal_NoActiveSession(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	b.sessionID = ""

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "no active session")
}

func TestACPTerminal_NegativeOutputByteLimitRejected(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "echo hi",
		"cwd":             b.workingDir,
		"outputByteLimit": -1,
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32602, errObj["code"])
	assert.Contains(t, errObj["message"], "outputByteLimit")
}

func TestACPTerminal_ZeroOutputByteLimitRetainsNothing(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	limit := 0

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId":       "sess-1",
		"command":         "printf 'abcdefgh'",
		"cwd":             b.workingDir,
		"outputByteLimit": limit,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	outResult := resps[2]["result"].(map[string]interface{})
	assert.Equal(t, true, outResult["truncated"])
	assert.Equal(t, "", outResult["output"])

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_ArgvStyleCommand(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf",
		"args":      []string{"argv-ok"},
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	require.Nil(t, resps[0]["error"])
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "argv-ok")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_EnvOverridesApplied(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "printf '%s' \"$LEAPMUX_ACP_TERM_TEST\"",
		"cwd":       b.workingDir,
		"env": []map[string]string{
			{"name": "LEAPMUX_ACP_TERM_TEST", "value": "from-host"},
		},
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	assert.Contains(t, resps[2]["result"].(map[string]interface{})["output"], "from-host")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_NonZeroExitMarksFailed(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "exit 7",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2)
	assert.EqualValues(t, 7, resps[1]["result"].(map[string]interface{})["exitCode"])

	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusFailed, row.Status)

	dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 3)
}

// The registry row must reach its final status BEFORE anything can observe the
// exit. sess.done is that observation point: terminal/wait_for_exit replies off
// it, so a client that closes the channel first can read its own terminal's row
// and still see RUNNING. This asserts the order at the one instant that shows
// it -- inside CloseBackgroundTask, where done must still be open.
func TestACPTerminal_ClosesTheRegistryRowBeforeTheExitIsObservable(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	var exitWasObservable atomic.Bool
	sink.OnCloseBackgroundTask = func(rowKey string, _ bgtask.Status) {
		sess, ok := b.getTerminal(rowKey)
		if !ok {
			return
		}
		select {
		case <-sess.done:
			exitWasObservable.Store(true)
		default:
		}
	}

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "exit 7",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2)
	require.EqualValues(t, 7, resps[1]["result"].(map[string]interface{})["exitCode"])

	assert.False(t, exitWasObservable.Load(),
		"wait_for_exit can reply while the row still says RUNNING")

	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusFailed, row.Status)

	dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 3)
}

func TestACPTerminal_OutputBeforeExitOmitsExitStatus(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	row, ok := sink.BackgroundTask(termID)
	require.True(t, ok, "create must upsert a background-task row")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a still-running sleep must stay Running")

	dispatchTerminal(b, acpMethodTerminalOutput, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 2)
	outResult := resps[1]["result"].(map[string]interface{})
	_, hasExit := outResult["exitStatus"]
	assert.False(t, hasExit, "in-flight terminals must omit exitStatus")

	dispatchTerminal(b, acpMethodTerminalRelease, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 3)
}

func TestACPTerminal_WaitForExitDoesNotBlockCaller(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 1",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
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
	resps = rec.wait(t, 3)
	ids := make([]float64, 0, 2)
	for _, r := range resps[1:] {
		ids = append(ids, r["id"].(float64))
	}
	assert.Contains(t, ids, float64(3), "kill must be handled while wait_for_exit is outstanding")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_KillAndReleaseUnknownID(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalKill, 1, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_gone",
	})
	dispatchTerminal(b, acpMethodTerminalRelease, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": "term_gone",
	})
	resps := rec.wait(t, 2)
	assert.EqualValues(t, -32602, resps[0]["error"].(map[string]interface{})["code"])
	assert.EqualValues(t, -32602, resps[1]["error"].(map[string]interface{})["code"])
}

func TestACPTerminal_MalformedParams(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	line, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  acpMethodTerminalCreate,
		"params":  "not-an-object",
	})
	b.handleACPOutput(providerkit.ParseLine(line))
	resps := rec.wait(t, 1)
	assert.EqualValues(t, -32602, resps[0]["error"].(map[string]interface{})["code"])
}

func TestACPTerminal_MissingJSONRPCIDIsIgnored(t *testing.T) {
	sink := &agenttest.Sink{}
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
	b.handleACPOutput(providerkit.ParseLine(line))

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
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.SetStdinForTest(struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw})
	b.SimulateExitForTest()

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		// sleep inherits the host pipes; without process-group kill, Stop
		// would hang waiting for stdout EOF after killing only /bin/sh.
		"command": "sleep 60",
		"cwd":     b.workingDir,
	})
	_ = rec.wait(t, 1)

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
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	b.SetStdinForTest(struct {
		io.Writer
		io.Closer
	}{Writer: io.MultiWriter(rec, pw), Closer: pw})
	b.SimulateExitForTest()

	b.Stop()

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "echo hi",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	errObj := resps[0]["error"].(map[string]interface{})
	assert.EqualValues(t, -32603, errObj["code"])
	assert.Contains(t, errObj["message"], "stopped")
}

func TestACPTerminal_ReleaseSessionOnClearContext(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	// newSessionLocked needs a cancelled ctx so SendRequest fails fast after
	// releaseSessionTerminals has already run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.SetContextForTest(ctx)
	b.SimulateExitForTest()

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	_, clearErr := b.ClearContext()
	assert.Error(t, clearErr, "session/new must fail without a live agent")

	b.terminalsMu.Lock()
	_, still := b.terminals[termID]
	closed := b.terminalsClosed
	b.terminalsMu.Unlock()
	assert.False(t, still)
	assert.False(t, closed, "ClearContext must not latch terminals closed")

	row, _ := sink.BackgroundTask(termID)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestACPTerminal_BaseEnvPinsWorkerMarker(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)
	t.Setenv("LEAPMUX_WORKER", "0")
	t.Setenv("GOOSE_TERMINAL", "1")

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   `printf '%s|%s' "$LEAPMUX_WORKER" "${GOOSE_TERMINAL-}"`,
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalOutput, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
	out := resps[2]["result"].(map[string]interface{})["output"].(string)
	assert.Equal(t, "1|", out, "FinalizeAgentEnv must pin LEAPMUX_WORKER and scrub GOOSE_TERMINAL")

	dispatchTerminal(b, acpMethodTerminalRelease, 4, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 4)
}

func TestACPTerminal_KillReportsSignalName(t *testing.T) {
	sink := &agenttest.Sink{}
	b, rec := newTerminalTestBase(t, sink)

	dispatchTerminal(b, acpMethodTerminalCreate, 1, map[string]interface{}{
		"sessionId": "sess-1",
		"command":   "sleep 30",
		"cwd":       b.workingDir,
	})
	resps := rec.wait(t, 1)
	termID := resps[0]["result"].(map[string]interface{})["terminalId"].(string)

	dispatchTerminal(b, acpMethodTerminalKill, 2, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	_ = rec.wait(t, 2)

	dispatchTerminal(b, acpMethodTerminalWaitForExit, 3, map[string]interface{}{
		"sessionId":  "sess-1",
		"terminalId": termID,
	})
	resps = rec.wait(t, 3)
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
	_ = rec.wait(t, 4)
}
