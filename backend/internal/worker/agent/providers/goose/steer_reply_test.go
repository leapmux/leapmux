package goose

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func gooseSessionUpdateFrame(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update":    update,
		},
	})
	require.NoError(t, err)
	return raw
}

func newGooseSteerPeer(t *testing.T, onSteer func(a *Agent, id int64, params json.RawMessage)) (*Agent, *agenttest.ControlSink, func(result json.RawMessage)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe := io.Pipe()
	a := &Agent{}
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.AttachPeerForTest(ctx, cancel, writePipe, "test-agent", "goose-session")
	a.HooksForTest().SessionMetadataHandler = a.captureSteerRunID
	a.HooksForTest().PromptEnded = a.finishGoosePrompt
	a.SetSteerMethodForTest(gooseSteerMethod)

	promptIDCh := make(chan int64, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		scanner := bufio.NewScanner(readPipe)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			var id int64
			if json.Unmarshal(req.ID, &id) != nil {
				continue
			}
			switch req.Method {
			case acp.MethodSessionPrompt:
				select {
				case promptIDCh <- id:
				default:
				}
			case gooseSteerMethod:
				onSteer(a, id, req.Params)
			}
		}
	})
	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
		wg.Wait()
	})
	finishPrompt := func(result json.RawMessage) {
		id := <-promptIDCh
		a.Deliver(id, agenttest.JSONRPCResponse(id, agenttest.RPCReply{Result: result}))
	}
	return a, sink, finishPrompt
}

func TestGooseSteerBindsExactReplyAtUserMessageChunkAndDropsUnreadOnPromptEnd(t *testing.T) {
	t.Parallel()
	steerCount := 0
	a, sink, finishPrompt := newGooseSteerPeer(t, func(a *Agent, id int64, _ json.RawMessage) {
		steerCount++
		msgID := "steer_1"
		if steerCount == 2 {
			msgID = "steer_2"
		}
		a.Deliver(id, agenttest.JSONRPCResponse(id, agenttest.RPCReply{
			Result: json.RawMessage(`{"runId":"run-1","messageId":"` + msgID + `"}`),
		}))
	})

	reporter, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok, "Goose must report steer resolution at native echo")
	assert.True(t, reporter.SteerRowAtEcho())

	lead, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, lead.TurnID)

	a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"goose": map[string]any{"activeRunId": "run-1"}},
	}))
	a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     "assistant-before",
		"content":       map[string]any{"type": "text", "text": "Before steer."},
	}))

	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Include changelog.",
	}))
	a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"goose": map[string]any{"queuedSteer": map[string]any{"messageId": "steer_1", "runId": "run-1"}}},
	}))
	assert.Empty(t, sink.SteerResolutions(), "queuedSteer must not resolve before user_message_chunk echo")

	// Emit the steered UserMessageChunk twice (multi-block message with same ID).
	for range 2 {
		a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
			"sessionUpdate": "user_message_chunk",
			"messageId":     "steer_1",
			"content":       map[string]any{"type": "text", "text": "Include changelog."},
			"_meta":         map[string]any{"goose": map[string]any{"messageId": "steer_1", "steer": true}},
		}))
	}
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: "goose-session",
			InputID:   "steer_1",
			TurnID:    lead.TurnID,
		},
	}, sink.SteerResolutions()[0])

	// Queue a second steer that Goose discards when the run ends.
	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Content:   "Discarded at run end.",
	}))
	finishPrompt(json.RawMessage(`{"stopReason":"end_turn"}`))
	testutil.RequireEventually(t, func() bool {
		return len(sink.SteerResolutions()) == 2
	})
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[1])
}

func TestGooseEarlySteerUserChunkCommitsWhenSteerRPCReturns(t *testing.T) {
	t.Parallel()
	var lead agent.NativeInputReceipt
	a, sink, finishPrompt := newGooseSteerPeer(t, func(a *Agent, id int64, _ json.RawMessage) {
		// Emit the user_message_chunk before the RPC response returns.
		a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
			"sessionUpdate": "user_message_chunk",
			"messageId":     "steer_early",
			"content":       map[string]any{"type": "text", "text": "Early steer."},
			"_meta":         map[string]any{"goose": map[string]any{"messageId": "steer_early", "steer": true}},
		}))
		a.Deliver(id, agenttest.JSONRPCResponse(id, agenttest.RPCReply{
			Result: json.RawMessage(`{"runId":"run-1","messageId":"steer_early"}`),
		}))
	})
	defer func() {
		finishPrompt(json.RawMessage(`{"stopReason":"end_turn"}`))
	}()

	var err error
	lead, err = a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(gooseSessionUpdateFrame(t, "goose-session", map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"goose": map[string]any{"activeRunId": "run-1"}},
	}))
	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-early",
		AttemptID: "attempt-early",
		Content:   "Early steer.",
	}))
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-early",
		AttemptID: "attempt-early",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: "goose-session",
			InputID:   "steer_early",
			TurnID:    lead.TurnID,
		},
	}, sink.SteerResolutions()[0])
}
