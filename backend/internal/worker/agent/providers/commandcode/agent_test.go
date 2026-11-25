package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
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
		sink:           agent.NewProviderServices(sink), clock: quartz.NewReal(), sessionID: "native-session", model: "native-model", permissionMode: "default", children: make(map[string]*childState),
		replyProcessID: id.Generate(),
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	t.Cleanup(func() { a.Stop(); cancel() })
	return a, sink
}

type rpcTestPeer struct {
	t          *testing.T
	a          *Agent
	handle     func(string, json.RawMessage) agenttest.RPCReply
	afterReply func(string)
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
	if p.afterReply != nil {
		p.afterReply(request.Method)
	}
	return len(data), nil
}

func (p *rpcTestPeer) Close() error { return nil }

func agentWithPeer(t *testing.T, handle func(string, json.RawMessage) agenttest.RPCReply, afterReply ...func(string)) *Agent {
	t.Helper()
	a, _ := testAgent(t)
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan struct{})
	peer := &rpcTestPeer{t: t, a: a, handle: handle}
	if len(afterReply) > 0 {
		peer.afterReply = afterReply[0]
	}
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
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "native steer"}))
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

func commandCodeSteerAgent(t *testing.T, reply func(string, json.RawMessage) agenttest.RPCReply, afterReply ...func(string)) (*Agent, *agenttest.Sink) {
	t.Helper()
	if reply == nil {
		reply = func(string, json.RawMessage) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1","queued":1}`)}
		}
	}
	a := agentWithPeer(t, reply, afterReply...)
	sink := &agenttest.Sink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
	return a, sink
}

func testCommandCodeSteer(index int, text string) agent.SteerRequest {
	return agent.SteerRequest{InputID: fmt.Sprintf("input-%d", index), AttemptID: fmt.Sprintf("attempt-%d", index), Content: text}
}

func testCommandCodeSteerReport(a *Agent, index int, turnID string, outcome agent.SteerOutcome) agent.SteerResolution {
	request := testCommandCodeSteer(index, "")
	report := agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID, Outcome: outcome}
	if outcome == agent.SteerOutcomeEchoed {
		report.Receipt = agent.NativeInputReceipt{SessionID: "native-session", TurnID: a.commandCodeDurableTurnID(turnID), InputID: "steer:" + request.AttemptID}
	}
	return report
}

func TestSteerDrainResolvesBothAcknowledgedInputsInOrder(t *testing.T) {
	queued := 0
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		queued++
		return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(`{"turnId":"turn_1","queued":%d}`, queued))}
	})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "before the steers"})
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "same steer")))
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(2, "same steer")))
	for _, count := range []int{0, -1} {
		feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": count})
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.SteerResolutions())
	feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": 2})
	require.Len(t, sink.Messages(), 1)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, "before the steers", text)
	assert.Equal(t, agent.MessageCompletionComplete, completion)
	assert.Equal(t, []agent.SteerResolution{
		testCommandCodeSteerReport(a, 1, "turn_1", agent.SteerOutcomeEchoed),
		testCommandCodeSteerReport(a, 2, "turn_1", agent.SteerOutcomeEchoed),
	}, sink.SteerResolutions())
}

func TestInterruptedTurnLeavesTheSteerToTheQueue(t *testing.T) {
	turnID := "turn_1"
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(`{"turnId":%q,"queued":1}`, turnID))}
	})
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "undelivered")))
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "interrupted", "leftover": []string{"undelivered"}})
	assert.Empty(t, sink.RequeuedInputs(), "the queue retains its original input instead of a copied input")
	assert.Empty(t, sink.SteerResolutions())
	assert.False(t, a.PublishTurnActive().Active)
	turnID = "turn_2"
	feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": turnID})
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(2, "new steer")))
	feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": 1})
	assert.Empty(t, sink.SteerResolutions(), "the ended turn cannot resolve the next input")
	feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": turnID, "count": 1})
	assert.Equal(t, []agent.SteerResolution{testCommandCodeSteerReport(a, 2, turnID, agent.SteerOutcomeEchoed)}, sink.SteerResolutions())
}

func TestSteerRejectionForgetsTheUnacceptedInput(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32001,"message":"the turn ended"}`)}
	})
	require.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "refused"}), agent.ErrNoActiveTurn)
	assert.Empty(t, a.pendingSteers)
	assert.Empty(t, sink.SteerResolutions())
}

func TestSteerDrainBeforeTheCallerReturnsWaitsForPrecedingWrites(t *testing.T) {
	var a *Agent
	a, sink := commandCodeSteerAgent(t, nil, func(string) {
		feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": 1})
	})
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "reason before the steer"})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "before the steer"})
	sink.PersistErr = assert.AnError
	defer func() { sink.PersistErr = nil }()
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "delivered")))
	assert.Empty(t, sink.SteerResolutions(), "the report waits behind the failed text write")
	failed := len(sink.Messages())
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, sink.Messages()[failed:], 2)
	assert.Contains(t, string(sink.Messages()[failed].Content), "reason before the steer")
	assert.Contains(t, string(sink.Messages()[failed+1].Content), "before the steer")
	assert.Equal(t, []agent.SteerResolution{testCommandCodeSteerReport(a, 1, "turn_1", agent.SteerOutcomeEchoed)}, sink.SteerResolutions())
}

func TestCommandCodeRejectsAnInconsistentSteerAcknowledgment(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, func(string, json.RawMessage) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1","queued":2}`)}
	})
	require.ErrorIs(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "ambiguous")), agent.ErrDeliveryUncertain)
	assert.False(t, a.PublishTurnActive().Steerable)
	assert.Empty(t, sink.SteerResolutions())
}

func TestCommandCodeCannotAttributeAnInconsistentDrain(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, nil)
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "tracked")))
	feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": 2})
	assert.Equal(t, []agent.SteerResolution{testCommandCodeSteerReport(a, 1, "", agent.SteerOutcomeUncertain)}, sink.SteerResolutions())
	assert.False(t, a.PublishTurnActive().Steerable)
}

func TestCommandCodeRawSteerInvalidatesTheTrackedQueue(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, nil)
	require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "tracked")))
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","id":42,"method":"turn/steer","Method":"session/state","params":{"input":"untracked"}}`)}))
	assert.Equal(t, []agent.SteerResolution{testCommandCodeSteerReport(a, 1, "", agent.SteerOutcomeUncertain)}, sink.SteerResolutions())
	require.ErrorIs(t, agenttest.SteerInput(a, testCommandCodeSteer(2, "later")), agent.ErrAgentBusy)
	feedMethod(t, a, methodSteersDelivered, map[string]any{"turnId": "turn_1", "count": 2})
	assert.Len(t, sink.SteerResolutions(), 1)
}

func TestCommandCodeRawSteerReadsEachNativeLine(t *testing.T) {
	assert.True(t, containsCommandCodeRawSteer([]byte("invalid\r\n{\"method\":\"turn/steer\"}\r\n")))
	assert.False(t, containsCommandCodeRawSteer([]byte("invalid\n{\"Method\":\"turn/steer\"}\n{\"method\":\"session/state\"}\n")))
}

func TestSteerInputRejectsAnIdleNativeSession(t *testing.T) {
	a, _ := testAgent(t)
	assert.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "native steer"}), agent.ErrNoActiveTurn)
}

func TestInputSessionRejectsEmptyAndReplacedHandles(t *testing.T) {
	a, _ := testAgent(t)
	for _, handle := range []string{"", "other-session"} {
		assert.Error(t, agenttest.SendInputForSession(a, handle, "native prompt", nil))
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
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, agenttest.SendInput(a, "later turn", nil))
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
	assert.ErrorIs(t, agenttest.SendInput(a, "Keep this queued input.", nil), agent.ErrAgentBusy)
}

var _ io.WriteCloser = (*rpcTestPeer)(nil)
