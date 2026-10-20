package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdvertisedACPSteerMethodDetection pins how ParseAdvertisedMethod reads
// the steer method that an initialize response advertises under a namespace.
// Each provider that steers this way pins its own namespace and method.
func TestAdvertisedACPSteerMethodDetection(t *testing.T) {
	t.Parallel()

	const namespace, method = "vendor", "_vendor/session/steer"
	assert.Equal(t, method, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{"sessionSteer":{"method":"_vendor/session/steer"}}}}}`), namespace, method))
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{}}}}`), namespace, method),
		"a namespace that advertises no steer method")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"description":"_vendor/session/steer"}`), namespace, method),
		"the method text outside the capability")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"other":{"sessionSteer":{"method":"_vendor/session/steer"}}}}}`), namespace, method),
		"the method under another namespace")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{"sessionSteer":{"method":"_vendor/session/other"}}}}}`), namespace, method),
		"a method that this provider does not speak")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`not json`), namespace, method))
}

// The base refuses a send while a prompt runs, for each ACP provider: the send
// reaches no RPC, and the base republishes the turn on demand.
func TestACPSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a, requests := newTestAgentForRPC(t)
	a.sink = agent.NewProviderServices(sink)
	a.wireTurnActive()
	a.promptActive = true
	err := a.SendInput("later turn", nil)
	assert.Empty(t, requests(), "a refused send must reach no RPC")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
}

func TestPrepareSteerStoresTheBufferedTextFirst(t *testing.T) {
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "A1 ", "message"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "A2", "message"))
	// The manager calls this optional interface before the input queue reserves the steer row.
	if preparer, ok := any(b).(agent.SteerPreparer); ok {
		preparer.PrepareSteer()
	}
	require.NoError(t, b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"steer"}`)}, agent.SpanInfo{}))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "A3", "message"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:A1 A2", "text:A3"}, assembledTexts(t, sink.Messages()))
	require.Len(t, sink.Messages(), 3)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, sink.Messages()[1].Source)
}

func TestPrepareSteerWaitsForTheDispatch(t *testing.T) {
	assertACPFlushWaitsForDispatch(t, func(b *Base) { b.PrepareSteer() })
}

type advertisedSteerResultReader interface {
	SteerAdvertisedResult(string, []*leapmuxv1.Attachment) (json.RawMessage, error)
}

func TestSteerAdvertisedResultReturnsTheReply(t *testing.T) {
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		assert.Equal(t, "_vendor/session/steer", method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"disposition":"queued_followup","receipt":"native-receipt"}`)}
	})
	a.steerMethod, a.promptActive = "_vendor/session/steer", true
	reader, ok := any(a).(advertisedSteerResultReader)
	require.True(t, ok, "the ACP base must expose the native steer result")
	raw, err := reader.SteerAdvertisedResult("steer", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"disposition":"queued_followup","receipt":"native-receipt"}`, string(raw))
	calls := requests()
	require.Len(t, calls, 1)
	assert.Equal(t, "session-1", calls[0].Params["sessionId"])
	assert.Equal(t, []interface{}{map[string]interface{}{"type": "text", "text": "steer"}}, calls[0].Params["prompt"])
}

func TestSteerAdvertisedResultKeepsAdmissionChecks(t *testing.T) {
	for _, scenario := range []string{"unsupported", "idle", "invalid request", "invalid params"} {
		t.Run(scenario, func(t *testing.T) {
			a, requests := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
				code := -32600
				if scenario == "invalid params" {
					code = -32602
				}
				return agenttest.RPCReply{Error: json.RawMessage(fmt.Sprintf(`{"code":%d,"message":"The turn ended."}`, code))}
			})
			a.steerMethod, a.promptActive = "_vendor/session/steer", true
			if scenario == "unsupported" {
				a.steerMethod = ""
			}
			if scenario == "idle" {
				a.promptActive = false
			}
			reader, ok := any(a).(advertisedSteerResultReader)
			require.True(t, ok)
			raw, err := reader.SteerAdvertisedResult("steer", nil)
			assert.Empty(t, raw)
			if scenario == "unsupported" {
				require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
			} else {
				require.ErrorIs(t, err, agent.ErrNoActiveTurn)
			}
			if scenario == "unsupported" || scenario == "idle" {
				assert.Empty(t, requests())
			} else {
				assert.Len(t, requests(), 1)
			}
		})
	}
}

func TestSteerAdvertisedResultKeepsTheSessionAndRechecksTheTurn(t *testing.T) {
	var a *testAgent
	ready := make(chan struct{})
	a, _ = newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		<-ready
		if a.sessionMu.TryLock() {
			a.sessionMu.Unlock()
			t.Error("session replacement could overtake the steer reply")
		}
		a.Mu.Lock()
		a.promptActive = false
		a.Mu.Unlock()
		return agenttest.RPCReply{Result: json.RawMessage(`{"disposition":"accepted"}`)}
	})
	a.steerMethod, a.promptActive = "_vendor/session/steer", true
	close(ready)
	raw, err := a.SteerAdvertisedResult("steer", nil)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.Empty(t, raw)
}

func TestSteerAdvertisedResultKeepsAnotherNativeRefusal(t *testing.T) {
	a, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The native request was refused."}`)}
	})
	a.steerMethod, a.promptActive = "_vendor/session/steer", true
	raw, err := a.SteerAdvertisedResult("steer", nil)
	require.ErrorContains(t, err, "The native request was refused.")
	assert.NotErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, raw)
}
