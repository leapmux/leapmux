package commandcode

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
