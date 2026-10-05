package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type geminiModelRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params struct {
		SessionID string `json:"sessionId"`
		ModelID   string `json:"modelId"`
	} `json:"params"`
}

type geminiModelPeer struct {
	receive func(geminiModelRequest)
}

// This peer can publish a later native event before the request write completes.
// The ordinary ACP peer sends its reply after its responder returns.
func (peer *geminiModelPeer) Write(raw []byte) (int, error) {
	var request geminiModelRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return 0, err
	}
	if peer.receive == nil {
		return 0, errors.New("the Gemini model peer has no request handler")
	}
	peer.receive(request)
	return len(raw), nil
}

func (*geminiModelPeer) Close() error { return nil }

func newGeminiModelPeer(t *testing.T) (*Agent, *geminiModelPeer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	a := &Agent{}
	peer := &geminiModelPeer{}
	a.AttachPeerForTest(ctx, cancel, peer, "gemini-model-test", "model-test-session")
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetCurrentModel("initial-model")
	t.Cleanup(func() {
		a.SimulateExitForTest()
		a.Process.Stop()
		cancel()
	})
	return a, peer
}

func TestGeminiNativeModelWriterUsesItsExactSessionAndModel(t *testing.T) {
	t.Parallel()
	a, peer := newGeminiModelPeer(t)
	var requests []geminiModelRequest
	peer.receive = func(request geminiModelRequest) {
		requests = append(requests, request)
		assert.True(t, a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	}
	require.NoError(t, a.setNativeModel("gemini-3.8-flash"))
	require.Len(t, requests, 1)
	assert.Equal(t, acp.MethodSessionSetModel, requests[0].Method)
	assert.Equal(t, "model-test-session", requests[0].Params.SessionID)
	assert.Equal(t, "gemini-3.8-flash", requests[0].Params.ModelID)
	assert.Equal(t, "gemini-3.8-flash", a.ModelForTest())
	assert.Equal(t, "model-test-session", a.CurrentSessionID())
}

func TestGeminiNativeModelWriterRejectsFailedAndInvalidReplies(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`null`, `[]`, `true`, `0`, `"accepted"`, `{`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			a, peer := newGeminiModelPeer(t)
			peer.receive = func(request geminiModelRequest) {
				a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(raw)}))
			}
			require.Error(t, a.setNativeModel("requested-model"))
			assert.Equal(t, "initial-model", a.ModelForTest())
		})
	}
	t.Run("native protocol failure", func(t *testing.T) {
		t.Parallel()
		a, peer := newGeminiModelPeer(t)
		peer.receive = func(request geminiModelRequest) {
			a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"Invalid native model"}`)}))
		}
		require.ErrorContains(t, a.setNativeModel("requested-model"), "Invalid native model")
		assert.Equal(t, "initial-model", a.ModelForTest())
	})
	t.Run("write failure", func(t *testing.T) {
		t.Parallel()
		a, _ := newGeminiModelPeer(t)
		a.SetStdinForTest(agenttest.FailingStdin{})
		require.Error(t, a.setNativeModel("requested-model"))
		assert.Equal(t, "initial-model", a.ModelForTest())
	})
}

func TestGeminiNativeModelWriterRejectsAForeignSessionReply(t *testing.T) {
	t.Parallel()
	a, peer := newGeminiModelPeer(t)
	peer.receive = func(request geminiModelRequest) {
		a.SetSessionIDForTest("replacement-session")
		a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)}))
	}
	require.ErrorContains(t, a.setNativeModel("requested-model"), "current native session")
	assert.Equal(t, "initial-model", a.ModelForTest())
}

func TestGeminiNativeModelWriterPreservesANewerReaderUpdate(t *testing.T) {
	t.Parallel()
	a, peer := newGeminiModelPeer(t)
	peer.receive = func(request geminiModelRequest) {
		a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)}))
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"model-test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"newer-model","options":[{"value":"newer-model","name":"Newer Model"}]}]}}}`))
	}
	require.NoError(t, a.setNativeModel("requested-model"))
	assert.Equal(t, "newer-model", a.ModelForTest(), "a later native reader update must survive the setter return")
}

func TestGeminiNativeModelWriterIgnoresALateReplyAfterCancellation(t *testing.T) {
	t.Parallel()
	a, peer := newGeminiModelPeer(t)
	var id int64
	peer.receive = func(request geminiModelRequest) {
		id = request.ID
		a.CancelForTest()
	}
	require.ErrorIs(t, a.setNativeModel("requested-model"), context.Canceled)
	assert.False(t, a.Deliver(id, agenttest.JSONRPCResponse(id, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	assert.Equal(t, "initial-model", a.ModelForTest())
}

func TestGeminiNativeModelWriterRejectsEmptyModelOrMissingSession(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	for _, model := range []string{"", " \n\t"} {
		require.ErrorContains(t, a.setNativeModel(model), "requires a model ID")
	}
	require.ErrorContains(t, a.setNativeModel("requested-model"), "no native session")
}

func TestGeminiModeReplyRejectsInvalidForeignAndFailedAcknowledgments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		session string
		result  string
		err     error
	}{
		{"root", "{}", errors.New("native protocol error")},
		{"foreign", "{}", nil},
		{"root", "null", nil},
		{"root", "[]", nil},
		{"root", "{", nil},
	} {
		a := &Agent{}
		a.SetSessionIDForTest("root")
		a.SetPermissionModeForTest(contracts.GeminiModeDefault)
		assert.False(t, a.observeModeSetterReply(test.session, contracts.GeminiModeYolo, json.RawMessage(test.result), test.err, a.SetPermissionMode))
		assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest())
		assert.Zero(t, a.modeGeneration)
	}
}

func TestGeminiModeReplyKeepsALaterNativeToolModeChange(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetSessionIDForTest("root")
	a.SetPermissionModeForTest(contracts.GeminiModeDefault)
	a.SetAvailableModesForTest(geminiModes())
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	require.True(t, a.observeModeSetterReply("root", contracts.GeminiModeYolo, json.RawMessage(`{}`), nil, a.SetPermissionMode))
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__later-call","status":"in_progress"}`))
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"enter_plan_mode__later-call","status":"completed"}`))
	a.observeNativeMode("root", json.RawMessage(`{"id":"enter_plan_mode__later-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`))
	assert.Equal(t, contracts.GeminiModePlan, a.PermissionModeForTest())
}

// A mode change makes Gemini send `[MODE_UPDATE] <mode>` as a text chunk
// before the session/set_mode reply: setMode calls config.setApprovalMode,
// whose event handleApprovalModeChanged sends the text (acpSession.ts). The
// text is no answer of the model, also while a prompt runs. Before this test,
// a mode change during a turn put the text into the answer of that turn.
func TestGeminiModeSetterEchoIsNoConversation(t *testing.T) {
	t.Parallel()
	const echo = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"model-test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] %s"}}}}`
	for name, tc := range map[string]struct {
		echoed string
		// buffered is the text that the turn holds after the setter returns.
		buffered string
	}{
		"the echo of the requested mode": {echoed: contracts.GeminiModeYolo},
		"the text of another mode":       {echoed: contracts.GeminiModePlan, buffered: "[MODE_UPDATE] plan"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, peer := newGeminiModelPeer(t)
			a.SetPromptActiveForTest(true)
			a.HooksForTest().SessionUpdateHandler = a.handleSessionUpdate
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.SetAvailableModesForTest(geminiModes())
			a.SetPermissionModeForTest(contracts.GeminiModeDefault)
			peer.receive = func(request geminiModelRequest) {
				a.HandleOutput(fmt.Appendf(nil, echo, tc.echoed))
				a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)}))
			}

			require.NoError(t, a.setNativePermissionMode(contracts.GeminiModeYolo, a.SetPermissionMode))

			assert.Equal(t, contracts.GeminiModeYolo, a.PermissionModeForTest())
			assert.Equal(t, tc.buffered, a.TurnAssistantTextForTest().String())
			// The reply ended the window of the echo, so the same text is the
			// model's text again.
			a.TurnAssistantTextForTest().Reset()
			a.HandleOutput(fmt.Appendf(nil, echo, contracts.GeminiModeYolo))
			assert.Equal(t, "[MODE_UPDATE] yolo", a.TurnAssistantTextForTest().String())
		})
	}
}

// A setter whose reply never comes ends the window of its echo too.
func TestGeminiModeSetterEchoEndsWithAFailedWrite(t *testing.T) {
	t.Parallel()
	a, _ := newGeminiModelPeer(t)
	a.SetPromptActiveForTest(true)
	a.HooksForTest().SessionUpdateHandler = a.handleSessionUpdate
	a.SetAvailableModesForTest(geminiModes())
	a.SetStdinForTest(agenttest.FailingStdin{})

	require.Error(t, a.setNativePermissionMode(contracts.GeminiModeYolo, a.SetPermissionMode))

	assert.False(t, a.handleSessionUpdate("model-test-session", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] yolo"}}`)),
		"no setter waits, so the text is the model's")
}

func TestGeminiModeWriterRefusesEmptyAndUnavailableModesBeforeTransport(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetSessionIDForTest("root")
	a.SetAvailableModesForTest(geminiModes())
	for _, mode := range []string{"", "autoedit", "unknown"} {
		assert.ErrorContains(t, a.setNativePermissionMode(mode, a.SetPermissionMode), "does not offer")
	}
	a.SetSessionIDForTest("")
	assert.ErrorContains(t, a.setNativePermissionMode(contracts.GeminiModeDefault, a.SetPermissionMode), "no native session")
}

func TestGeminiHistoricalModeToolCannotChangeTheNewSessionMode(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetSessionIDForTest("root")
	a.SetPermissionModeForTest(contracts.GeminiModeDefault)
	a.SetAvailableModesForTest(geminiModes())
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	assert.False(t, a.PromptActive(), "native load replay has no active model turn")
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"enter_plan_mode__historical-call","status":"completed"}`))
	a.observeNativeMode("root", json.RawMessage(`{"id":"enter_plan_mode__historical-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`))
	assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest(), "historical native records cannot restore a runtime mode")
}
