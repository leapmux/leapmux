//go:build unix

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexInterruptRig captures every JSON-RPC frame Codex writes to stdin and
// supplies the delayed response for a turn/interrupt request.
type codexInterruptRig struct {
	agent          *Agent
	responseBodies chan agenttest.RPCReply
	captured       func() []map[string]any
}

func newCodexInterruptRig(t *testing.T) *codexInterruptRig {
	return newCodexInterruptRigWithAutoResponse(t, true)
}

func newCodexInterruptRigWithAutoResponse(t *testing.T, autoRespond bool) *codexInterruptRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "test-agent",
			ProviderName: "codex",
			Stdin:        writePipe,
			Ctx:          ctx,
			Cancel:       cancel,
			ProcessDone:  make(chan struct{}),
			StderrDone:   make(chan struct{}),
			APITimeout:   2 * time.Second,
		})},
	}
	a.SkipStderr()

	var (
		mu       sync.Mutex
		captured []map[string]any
	)
	responseBodies := make(chan agenttest.RPCReply, 1)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var frame struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				continue
			}
			mu.Lock()
			captured = append(captured, map[string]any{
				"jsonrpc": "2.0",
				"id":      frame.ID,
				"method":  frame.Method,
				"params":  frame.Params,
			})
			mu.Unlock()
			if frame.ID != 0 && autoRespond {
				body := agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				select {
				case body = <-responseBodies:
				default:
				}
				a.Deliver(frame.ID, agenttest.JSONRPCResponse(frame.ID, body))
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
	})

	return &codexInterruptRig{
		agent:          a,
		responseBodies: responseBodies,
		captured: func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			out := make([]map[string]any, len(captured))
			copy(out, captured)
			return out
		},
	}
}

func TestCodexAgent_Interrupt_TimesOutWhenCodexDoesNotAnswer(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRigWithAutoResponse(t, false)
	rig.agent.threadID = "thread-A"
	rig.agent.turnID = "turn-42"
	rig.agent.SetAPITimeoutForTest(20 * time.Millisecond)

	err := rig.agent.Interrupt()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout waiting for turn/interrupt response")
	require.Len(t, rig.captured(), 1)
}

func TestCodexAgent_Interrupt_CoalescesConcurrentRequests(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRigWithAutoResponse(t, false)
	rig.agent.threadID = "thread-A"
	rig.agent.turnID = "turn-42"
	rig.agent.SetAPITimeoutForTest(time.Second)

	results := make(chan error, 2)
	go func() { results <- rig.agent.Interrupt() }()
	go func() { results <- rig.agent.Interrupt() }()

	require.Eventually(t, func() bool { return len(rig.captured()) == 1 }, time.Second, 5*time.Millisecond)
	frames := rig.captured()
	require.Len(t, frames, 1, "concurrent calls must share one request")
	requestID, ok := frames[0]["id"].(int64)
	require.True(t, ok)
	require.True(t, rig.agent.Deliver(requestID, agenttest.JSONRPCResponse(requestID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	assert.Len(t, rig.captured(), 1)
}

func TestCodexAgent_Interrupt_ReturnsRPCError(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "thread-A"
	rig.agent.turnID = "turn-42"
	rig.responseBodies <- agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"turn is not active"}`)}

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "turn is not active")
}

func TestCodexAgent_Interrupt_SendsTurnInterruptRequest(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "thread-A"
	rig.agent.turnID = "turn-42"

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- rig.agent.Interrupt()
	}()

	// The response arrives after Codex aborts the turn. The test rig sends that
	// response as soon as it captures the request.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rig.captured()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-interruptDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("turn/interrupt did not receive a response")
	}
	frames := rig.captured()
	require.Len(t, frames, 1)
	assert.Equal(t, "2.0", frames[0]["jsonrpc"])
	assert.Equal(t, "turn/interrupt", frames[0]["method"])
	assert.NotZero(t, frames[0]["id"], "turn/interrupt must be a request")

	params, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "thread-A", params["threadId"])
	assert.Equal(t, "turn-42", params["turnId"])
}

func TestCodexAgent_InterruptChild_SendsTurnInterruptRequest(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.collabChildren = map[string]*codexChildState{"child-thread": {spawnCorrelationID: "spawn-1", turnID: "child-turn"}}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- rig.agent.InterruptChild("child-thread")
	}()
	select {
	case err := <-interruptDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("child turn/interrupt did not receive a response")
	}

	frames := rig.captured()
	require.Len(t, frames, 1)
	assert.NotZero(t, frames[0]["id"], "child turn/interrupt must be a request")
	assert.Equal(t, "turn/interrupt", frames[0]["method"])
	params, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "child-thread", params["threadId"])
	assert.Equal(t, "child-turn", params["turnId"])
}

func TestCodexAgent_InterruptChild_NoActiveTurnIsNoop(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.collabChildren = map[string]*codexChildState{"child-thread": {spawnCorrelationID: "spawn-1"}}

	require.NoError(t, rig.agent.InterruptChild("child-thread"))
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, rig.captured(), "an idle child must not send turn/interrupt without a turn ID")
}

func TestCodexAgent_Interrupt_UsesMainTurnAfterChildTurnStarted(t *testing.T) {
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.sink = agent.NewProviderServices(&agenttest.Sink{})
	rig.agent.threadID = "main-thread"

	handleCodexOutput(rig.agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	handleCodexOutput(rig.agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

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
	t.Parallel()

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
	t.Parallel()

	rig := newCodexInterruptRig(t)
	rig.agent.threadID = "t"
	rig.agent.turnID = "u"
	rig.agent.SetStoppedForTest(true)

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

func TestCodexAgent_SendInput_DuringTurnUsesMainTurnAfterChildTurnStarted(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:     agent.AgentID(),
		Stdin:       writePipe,
		Ctx:         ctx,
		Cancel:      cancel,
		ProcessDone: make(chan struct{}),
		StderrDone:  make(chan struct{}),
	})
	agent.SetAPITimeoutForTest(2 * time.Second)
	agent.SkipStderr()

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
			agent.Deliver(int64(id), agenttest.JSONRPCResponse(int64(id), agenttest.RPCReply{Result: json.RawMessage(`{}`)}))
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
	})

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	require.NoError(t, agent.SteerInput("steer this", nil))

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

func TestInterrupt_CodexWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "turn/interrupt",
		"params":  map[string]any{"threadId": "t", "turnId": "u"},
	})
	require.NoError(t, err)
	assert.True(t, codexProvider{}.IsInterrupt(string(b)),
		"codexProvider.IsInterrupt must recognise the frame Agent.Interrupt emits")
}
