package commandcode

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputBlocksKeepsTheRequiredEmptyTextField(t *testing.T) {
	t.Parallel()
	blocks, err := inputBlocks("", []agent.ClassifiedAttachment{{Kind: contracts.AttachmentMediaKindImage, Filename: "proof.png", MIMEType: "image/png", Data: []byte{0, 1, 2, 255}}})
	require.NoError(t, err)
	raw, err := json.Marshal(blocks)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":""},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC/w=="}}]`, string(raw))
}

func testAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan struct{})
	close(ended)
	sink := &agenttest.Sink{}
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "command-code-test", ProviderName: "Command Code", Ctx: ctx, Cancel: cancel, Stdin: &agenttest.Stdin{}, ProcessDone: ended})},
		sink:           agent.NewProviderServices(sink), clock: quartz.NewReal(), sessionID: "native-session", model: "native-model", permissionMode: "default", tools: make(map[string]openTool), children: make(map[string]*childState),
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	t.Cleanup(func() { a.Stop(); cancel() })
	return a, sink
}

type rpcTestPeer struct {
	t      *testing.T
	a      *Agent
	handle func(string, json.RawMessage) agenttest.RPCReply
}

func (p *rpcTestPeer) Write(data []byte) (int, error) {
	var request struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	reply := p.handle(request.Method, request.Params)
	p.a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, reply))
	return len(data), nil
}

func (p *rpcTestPeer) Close() error { return nil }

func agentWithPeer(t *testing.T, handle func(string, json.RawMessage) agenttest.RPCReply) *Agent {
	t.Helper()
	a, _ := testAgent(t)
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan struct{})
	peer := &rpcTestPeer{t: t, a: a, handle: handle}
	a.JSONRPCProcess = providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "peer-test", ProviderName: "Command Code", Ctx: ctx, Cancel: cancel, Stdin: peer, ProcessDone: ended})}
	t.Cleanup(func() { close(ended); cancel() })
	return a
}

func feedEvent(t *testing.T, a *Agent, event map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "event", "event": event})
	require.NoError(t, err)
	a.HandleOutput(raw)
	return raw
}

func feedMethod(t *testing.T, a *Agent, method string, params map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	require.NoError(t, err)
	a.HandleOutput(raw)
	return raw
}

func TestSteerInputConfirmsNativeDelivery(t *testing.T) {
	a := agentWithPeer(t, func(method string, params json.RawMessage) agenttest.RPCReply {
		assert.Equal(t, methodTurnSteer, method)
		assert.JSONEq(t, `{"input":"native steer"}`, string(params))
		return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1","queued":1}`)}
	})
	a.turnID = "turn_1"
	require.NoError(t, a.SteerInput("native steer", nil))
}

func TestSteerDeclaresRowAtEchoWithoutSurvival(t *testing.T) {
	a, _ := testAgent(t)
	reporter, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok)
	assert.True(t, reporter.SteerRowAtEcho())
	survivor, ok := any(a).(agent.SteerSurvivor)
	require.True(t, ok)
	assert.False(t, survivor.SteerSurvivesTurnEnd())
}

func commandCodeSteerAgent(t *testing.T, reply func(string, json.RawMessage) agenttest.RPCReply) (*Agent, *agenttest.Sink) {
	t.Helper()
	if reply == nil {
		reply = func(string, json.RawMessage) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1","queued":1}`)}
		}
	}
	a := agentWithPeer(t, reply)
	sink := &agenttest.Sink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.startTurn("turn_1")
	return a, sink
}

func TestSteersDeliveredReportsTheEchoCount(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, nil)
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "before the steers"})
	require.NoError(t, a.SteerInput("first", nil))
	require.NoError(t, a.SteerInput("second", nil))
	for _, count := range []int{0, -1} {
		feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": count})
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.SteerEchoReports())
	// .tmp/probe/commandcode/run3 frame 48 reports the number of delivered steers.
	feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 2})
	require.Len(t, sink.Messages(), 1)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, "before the steers", text)
	assert.Equal(t, agent.MessageCompletionComplete, completion)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
}

func TestInterruptedTurnLeavesTheSteerToTheQueue(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, nil)
	require.NoError(t, a.SteerInput("undelivered", nil))
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "interrupted", "leftover": []string{"undelivered"}})
	assert.Empty(t, sink.RequeuedInputs(), "the queue retains its original input instead of a copied input")
	assert.Empty(t, sink.SteerEchoReports())
	assert.False(t, a.PublishTurnActive().Active)
	a.startTurn("turn_2")
	feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 1})
	assert.Empty(t, sink.SteerEchoReports(), "the ended turn leaves no stale ledger entry")
	require.NoError(t, a.SteerInput("new steer", nil))
	feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 99})
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerRejectionForgetsTheUnacceptedInput(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32001,"message":"the turn ended"}`)}
	})
	require.ErrorIs(t, a.SteerInput("refused", nil), agent.ErrNoActiveTurn)
	feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 1})
	assert.Empty(t, sink.SteerEchoReports())
}

func TestSteerEarlyEchoSurvivesAFailedWriteAndALaterRequestError(t *testing.T) {
	var a *Agent
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 1})
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32001,"message":"the turn ended after delivery"}`)}
	})
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "reason before the steer"})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "before the steer"})
	sink.PersistErr = assert.AnError
	defer func() { sink.PersistErr = nil }()
	require.NoError(t, a.SteerInput("delivered", nil), "the native echo proves delivery")
	assert.Empty(t, sink.SteerEchoReports(), "the report waits behind the failed text write")
	failed := len(sink.Messages())
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, sink.Messages()[failed:], 2)
	assert.Contains(t, string(sink.Messages()[failed].Content), "reason before the steer")
	assert.Contains(t, string(sink.Messages()[failed+1].Content), "before the steer")
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerInputRejectsAnIdleNativeSession(t *testing.T) {
	a, _ := testAgent(t)
	assert.ErrorIs(t, a.SteerInput("native steer", nil), agent.ErrNoActiveTurn)
}

func TestInputSessionRejectsEmptyAndReplacedHandles(t *testing.T) {
	a, _ := testAgent(t)
	for _, handle := range []string{"", "other-session"} {
		assert.Error(t, a.SendInputForSession(handle, "native prompt", nil))
	}
}

func TestClearContextRequiresANewNativeProcess(t *testing.T) {
	a, _ := testAgent(t)
	id, err := a.ClearContext()
	assert.Empty(t, id)
	assert.ErrorIs(t, err, agent.ErrContextClearUnsupported)
}

func TestCommandCodePublishTurnActiveRaisesItsToken(t *testing.T) {
	a, sink := testAgent(t)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestCommandCodeBusyRefusalRepublishesTheTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later turn", nil))
}

func TestCommandCodeRejectsMissingAndReplacedSessions(t *testing.T) {
	a, _ := testAgent(t)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

func TestNativeBusyResponseKeepsQueuedInputAvailable(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		assert.Equal(t, methodTurnStart, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32010,"message":"a turn is already running; use turn/steer or turn/interrupt"}`)}
	})
	assert.ErrorIs(t, a.SendInput("Keep this queued input.", nil), agent.ErrAgentBusy)
}

var _ io.WriteCloser = (*rpcTestPeer)(nil)
