//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedClaudeControl is a single control_request frame the
// claude interrupt rig captured from the agent's stdin.
type recordedClaudeControl struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// claudeInterruptRig wires a ClaudeCodeAgent to an in-memory pipe
// pair so the test can capture the interrupt control_request and
// hand the matching control_response back through stdout. Claude's
// sendControlAndWait blocks until the agent responds; without the
// echo this test would deadlock.
type claudeInterruptRig struct {
	agent    *ClaudeCodeAgent
	captured func() []recordedClaudeControl
}

func newClaudeInterruptRig(t *testing.T) *claudeInterruptRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stdinReader, stdinWriter, err := os.Pipe()
	require.NoError(t, err)
	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:      "test-agent",
			providerName: "claude",
			stdin:        stdinWriter,
			ctx:          ctx,
			cancel:       cancel,
			processDone:  make(chan struct{}),
			stderrDone:   make(chan struct{}),
			apiTimeout:   2 * time.Second,
		},
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	var (
		mu       sync.Mutex
		captured []recordedClaudeControl
	)

	// Read agent stdin → echo a control_response back through stdout.
	go func() {
		scanner := bufio.NewScanner(stdinReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var rec recordedClaudeControl
			if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
				continue
			}
			if rec.Type != "control_request" {
				continue
			}
			mu.Lock()
			captured = append(captured, rec)
			mu.Unlock()

			// Construct the matching control_response shape Claude
			// Code emits: response.subtype="success" terminates the
			// pending wait inside sendControlAndWait.
			resp := map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": rec.RequestID,
					"response":   map[string]any{},
				},
			}
			b, _ := json.Marshal(resp)
			b = append(b, '\n')
			if _, err := stdoutWriter.Write(b); err != nil {
				return
			}
		}
	}()

	// Drive the agent's read loop from the fake stdout. Mirrors
	// piTestRig — we don't call processBase.readOutput because it
	// ends with cmd.Wait() which would nil-deref without an
	// exec.Cmd.
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := parseLine(append([]byte(nil), scanner.Bytes()...))
			if a.handlePendingControlResponse(line) {
				continue
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		_ = stdinReader.Close()
		_ = stdoutReader.Close()
	})

	return &claudeInterruptRig{
		agent: a,
		captured: func() []recordedClaudeControl {
			mu.Lock()
			defer mu.Unlock()
			out := make([]recordedClaudeControl, len(captured))
			copy(out, captured)
			return out
		},
	}
}

func TestClaudeCodeAgent_Interrupt_SendsControlRequest(t *testing.T) {
	rig := newClaudeInterruptRig(t)

	require.NoError(t, rig.agent.Interrupt())

	captured := rig.captured()
	require.Len(t, captured, 1)
	rec := captured[0]
	assert.Equal(t, "control_request", rec.Type)
	assert.NotEmpty(t, rec.RequestID, "request_id must be populated for response correlation")

	var inner struct {
		Subtype string `json:"subtype"`
	}
	require.NoError(t, json.Unmarshal(rec.Request, &inner))
	assert.Equal(t, "interrupt", inner.Subtype,
		"Claude Code interrupt must use the {subtype:'interrupt'} control_request")
}

func TestClaudeCodeAgent_Interrupt_AfterStopErrors(t *testing.T) {
	rig := newClaudeInterruptRig(t)
	// Mark the agent stopped without driving the cmd lifecycle.
	rig.agent.mu.Lock()
	rig.agent.stopped = true
	rig.agent.mu.Unlock()

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// --- Codex ---

// codexInterruptRig captures every JSON-RPC frame Codex writes to
// stdin without bothering to round-trip a response (turn/interrupt
// is a fire-and-forget notification per the JSON-RPC spec).
type codexInterruptRig struct {
	agent    *CodexAgent
	captured func() []map[string]any
}

func newCodexInterruptRig(t *testing.T) *codexInterruptRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	a := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{
			agentID:      "test-agent",
			providerName: "codex",
			stdin:        writePipe,
			ctx:          ctx,
			cancel:       cancel,
			processDone:  make(chan struct{}),
			stderrDone:   make(chan struct{}),
			apiTimeout:   2 * time.Second,
		}},
	}
	close(a.stderrDone)

	var (
		mu       sync.Mutex
		captured []map[string]any
	)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var frame map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				continue
			}
			mu.Lock()
			captured = append(captured, frame)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
	})

	return &codexInterruptRig{
		agent: a,
		captured: func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			out := make([]map[string]any, len(captured))
			copy(out, captured)
			return out
		},
	}
}

func TestCodexAgent_Interrupt_SendsTurnInterruptNotification(t *testing.T) {
	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "thread-A"
	rig.agent.turnID = "turn-42"

	require.NoError(t, rig.agent.Interrupt())

	// Notifications are sent without waiting; allow the writer goroutine
	// to drain. The pipe is synchronous — a second-long bound is
	// generous for CI noise.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rig.captured()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	frames := rig.captured()
	require.Len(t, frames, 1)
	assert.Equal(t, "2.0", frames[0]["jsonrpc"])
	assert.Equal(t, "turn/interrupt", frames[0]["method"])
	// Notifications must NOT carry an id field per JSON-RPC 2.0 — the
	// running turn ends with its normal turn/completed and Codex
	// would log an error if our notification tried to elicit a reply.
	_, hasID := frames[0]["id"]
	assert.False(t, hasID, "notification frames must not carry id")

	params, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "thread-A", params["threadId"])
	assert.Equal(t, "turn-42", params["turnId"])
}

func TestCodexAgent_Interrupt_UsesMainTurnAfterChildTurnStarted(t *testing.T) {
	rig := newCodexInterruptRig(t)
	rig.agent.sink = &testSink{}
	rig.agent.threadID = "main-thread"

	handleCodexOutput(rig.agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	handleCodexOutput(rig.agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	require.NoError(t, rig.agent.Interrupt())

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rig.captured()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	frames := rig.captured()
	require.Len(t, frames, 1)
	assert.Equal(t, "turn/interrupt", frames[0]["method"])

	params, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "main-thread", params["threadId"])
	assert.Equal(t, "main-turn", params["turnId"])
}

func TestCodexAgent_Interrupt_NoTurnIsNoop(t *testing.T) {
	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "thread-A"
	// turnID intentionally empty: Codex hasn't started a turn yet,
	// or the turn already completed. Calling Interrupt unconditionally
	// must succeed without sending anything.

	require.NoError(t, rig.agent.Interrupt())

	// Give the pipe reader goroutine a chance to surface anything.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, rig.captured(),
		"Interrupt with no active turn must not emit a notification")
}

func TestCodexAgent_Interrupt_AfterStopErrors(t *testing.T) {
	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "t"
	rig.agent.turnID = "u"
	rig.agent.mu.Lock()
	rig.agent.stopped = true
	rig.agent.mu.Unlock()

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

func TestCodexAgent_SendInput_DuringTurnUsesMainTurnAfterChildTurnStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.stdin = writePipe
	agent.ctx = ctx
	agent.cancel = cancel
	agent.processDone = make(chan struct{})
	agent.stderrDone = make(chan struct{})
	agent.apiTimeout = 2 * time.Second
	close(agent.stderrDone)

	var (
		mu       sync.Mutex
		captured []map[string]any
	)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var frame map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				continue
			}
			mu.Lock()
			captured = append(captured, frame)
			mu.Unlock()

			id, ok := frame["id"].(float64)
			if !ok {
				continue
			}
			agent.deliver(int64(id), json.RawMessage(`{}`))
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
	})

	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	require.NoError(t, agent.SendInput("steer this", nil))

	mu.Lock()
	frames := append([]map[string]any(nil), captured...)
	mu.Unlock()
	require.Len(t, frames, 1)
	assert.Equal(t, "turn/steer", frames[0]["method"])

	params, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "main-thread", params["threadId"])
	assert.Equal(t, "main-turn", params["expectedTurnId"])
}

// --- Pi ---

func TestPiAgent_Interrupt_SendsAbortDuringActiveTurn(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.agent.mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.mu.Unlock()
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		// Pi acks abort with success:true and a null payload.
		return json.RawMessage(`null`), true, ""
	})

	require.NoError(t, rig.agent.Interrupt())

	reqs := rig.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, PiCommandAbort, reqs[0].Type)
	assert.NotEmpty(t, reqs[0].ID)
}

func TestPiAgent_Interrupt_NoActiveTurnIsNoop(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	// currentTurnActive defaults to false.

	require.NoError(t, rig.agent.Interrupt())

	// Allow potential writes to drain before asserting.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, rig.requests(),
		"Interrupt with no active turn must not write any command")
}

func TestPiAgent_Interrupt_AfterStopErrors(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.agent.mu.Lock()
	rig.agent.stopped = true
	rig.agent.currentTurnActive = true
	rig.agent.mu.Unlock()

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// --- ACP (covers Cursor, Copilot, Kilo, OpenCode, Goose
// because they all embed acpBase and inherit Interrupt verbatim) ---

func TestACPAgent_Interrupt_SendsSessionCancelNotification(t *testing.T) {
	agent, requests := newGooseAgentForRPCWithResponder(t,
		func(string) json.RawMessage { return json.RawMessage(`{}`) })
	// Helper sets sessionID="session-1" by default.

	require.NoError(t, agent.Interrupt())

	// session/cancel is a notification — drain briefly.
	deadline := time.Now().Add(time.Second)
	var got []recordedRequest
	for time.Now().Before(deadline) {
		got = requests()
		if len(got) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Len(t, got, 1)
	assert.Equal(t, "session/cancel", got[0].Method)
	assert.Equal(t, "session-1", got[0].Params["sessionId"])
}

func TestACPAgent_Interrupt_NoSessionIsNoop(t *testing.T) {
	agent, requests := newGooseAgentForRPCWithResponder(t,
		func(string) json.RawMessage { return json.RawMessage(`{}`) })
	// Wipe the session so cancelSession would emit a stale id; the
	// interrupt path must short-circuit instead of emitting at all.
	// (acpBase fields are reachable via the embedding promotion.)
	agent.mu.Lock()
	agent.sessionID = ""
	agent.mu.Unlock()

	require.NoError(t, agent.Interrupt())
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, requests(),
		"Interrupt before session/new completes must be a no-op")
}

func TestACPAgent_Interrupt_AfterStopErrors(t *testing.T) {
	agent, _ := newGooseAgentForRPCWithResponder(t,
		func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.mu.Lock()
	agent.stopped = true
	agent.mu.Unlock()

	err := agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// --- Wire-format reciprocity: Interrupt output must round-trip
// through the same provider's IsInterrupt classifier. This is the
// smoke test that catches a divergence between producer
// (`Interrupt()`) and detector (`provider.IsInterrupt(content)`)
// at compile-fail time rather than first incident.

func TestInterrupt_ClaudeWireFormatMatchesProviderClassifier(t *testing.T) {
	// Reconstruct the exact frame ClaudeCodeAgent.Interrupt produces
	// (sendControlAndWait formats it identically).
	frame := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`,
		"req-1", `{"subtype":"interrupt"}`)
	assert.True(t, claudeProvider{}.IsInterrupt(frame),
		"claudeProvider.IsInterrupt must recognise the frame ClaudeCodeAgent.Interrupt emits")
}

func TestInterrupt_CodexWireFormatMatchesProviderClassifier(t *testing.T) {
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "turn/interrupt",
		"params":  map[string]any{"threadId": "t", "turnId": "u"},
	})
	require.NoError(t, err)
	assert.True(t, codexProvider{}.IsInterrupt(string(b)),
		"codexProvider.IsInterrupt must recognise the frame CodexAgent.Interrupt emits")
}

func TestInterrupt_PiWireFormatMatchesProviderClassifier(t *testing.T) {
	// Pi's sendPiCommand wraps {type:"abort", id:...} — IsInterrupt
	// keys on type only.
	frame := `{"type":"abort","id":"leapmux-1"}`
	assert.True(t, piProvider{}.IsInterrupt(frame),
		"piProvider.IsInterrupt must recognise the frame PiAgent.Interrupt emits")
}

func TestInterrupt_ACPWireFormatMatchesProviderClassifier(t *testing.T) {
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": "session-1"},
	})
	require.NoError(t, err)
	assert.True(t, acpProvider{}.IsInterrupt(string(b)),
		"acpProvider.IsInterrupt must recognise the frame acpBase.Interrupt emits")
}
