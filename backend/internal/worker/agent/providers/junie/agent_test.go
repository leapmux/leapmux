package junie

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newJunieAgentForRPC wires a Junie agent to a fake peer that answers `{}` to
// every request, with the hooks Start configures.
func newJunieAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.HooksForTest().ModeSetter = a.SetModeViaConfigOption
			a.HooksForTest().ModelIDNormalizer = normalizeJunieModelID
			a.HooksForTest().ModelSetter = a.setJunieModel
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

// Junie reports a custom model profile by a decorated wire id and rejects the
// plain profile id on a model write. The two forms must normalize to one id, or
// a startup model apply fails and the worker relaunches the agent to retry it.
func TestJunieModelIDRoundTripsThroughTheDecoratedWireForm(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "custom:mock-model", normalizeJunieModelID("v1:6:custom:custom:mock-model"))
	assert.Equal(t, "v1:6:custom:custom:mock-model", junieModelIDForWire("custom:mock-model"))
	assert.Equal(t, "custom:mock-model", normalizeJunieModelID(junieModelIDForWire("custom:mock-model")),
		"the worker's own form is a fixed point of the round trip")

	// A non-profile id carries no decoration and travels unchanged.
	assert.Equal(t, "claude-sonnet", normalizeJunieModelID("claude-sonnet"))
	assert.Equal(t, "claude-sonnet", junieModelIDForWire("claude-sonnet"))

	// The source length and provider identify a proxy even when another proxy
	// offers the same model. Keep both wire IDs intact.
	for _, wire := range []string{
		"v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex",
		"v1:11:proxy:other:gpt-5.3-codex",
	} {
		assert.Equal(t, wire, normalizeJunieModelID(wire))
		assert.Equal(t, wire, junieModelIDForWire(wire))
	}
}

func TestJunieSetModelKeepsProxyProvidersDistinct(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)
	a.SetModelForTest("v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex")
	const other = "v1:11:proxy:other:gpt-5.3-codex"

	require.NoError(t, a.setJunieModel(other))

	assert.Equal(t, other, a.ModelForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, other, recorded[0].Params["value"])
}

func TestJunieSetModelCanLeaveAProxyForABareModel(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)
	const current = "v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex"
	a.SetModelForTest(current)
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: current, DisplayName: "Proxy model"}})

	require.NoError(t, a.setJunieModel("gpt-5.3-codex"))

	assert.Equal(t, "gpt-5.3-codex", a.ModelForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "gpt-5.3-codex", recorded[0].Params["value"])
}

// A live model change writes the decorated id and stores the plain one.
func TestJunieUpdateSettingsWritesTheDecoratedModel(t *testing.T) {
	t.Parallel()

	a, requests := newJunieAgentForRPC(t)
	a.SetModelForTest("claude-sonnet")

	updated := a.UpdateSettings(map[string]string{agent.OptionIDModel: "custom:mock-model"})
	require.True(t, updated.AppliedLive)
	require.Equal(t, "custom:mock-model", a.ModelForTest())

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, "v1:6:custom:custom:mock-model", recorded[0].Params["value"])
}

// Junie rejects session/set_mode ("use session/set_config_option with
// configId=\"mode\""), so a live mode change must take the config-option route.
func TestJunieUpdateSettingsSendsModeViaConfigOption(t *testing.T) {
	t.Parallel()

	a, requests := newJunieAgentForRPC(t)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: contracts.JunieModeDefault, Name: "Default"},
		{Id: contracts.JunieModePlan, Name: "Plan"},
	})
	a.SetPermissionModeForTest(contracts.JunieModeDefault)

	updated := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.JunieModePlan})
	require.True(t, updated.AppliedLive)
	require.Equal(t, contracts.JunieModePlan, a.PermissionModeForTest())

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method,
		"the mode write rides session/set_config_option, never session/set_mode")
	assert.Equal(t, acp.ConfigOptionIDMode, recorded[0].Params["configId"])
	assert.Equal(t, contracts.JunieModePlan, recorded[0].Params["value"])
}

// Junie steers through its own `_session/steering` method: the reply's
// outcome states what the session did with the input. Frames from
// .tmp/probe/junie/run2 and run6.
func TestJunieSteerSendsTheNativeMethod(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		outcome    string
		replyError *agenttest.RPCReply
		wantErr    error
	}{
		{"injected delivers", "injected", nil, nil},
		{"startedNewTurn delivers", "startedNewTurn", nil, nil},
		{"an invalid-params refusal answers no active turn", "", &agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"no active session turn"}`)}, agent.ErrNoActiveTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			outcome, replyError := tc.outcome, tc.replyError
			a, requests := acptest.NewAgentForRPCWithResponder(t,
				func() *Agent { return &Agent{} },
				func(a *Agent) *acp.Base { return &a.Base },
				func(method string) agenttest.RPCReply {
					if method == junieSteerMethod {
						if replyError != nil {
							return *replyError
						}
						return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"` + outcome + `"}`)}
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
			a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
			a.SetSessionIDForTest("junie-session")
			a.SetPromptActiveForTest(true)

			err := a.SteerInput("Steer text while active.", nil)
			assert.Equal(t, tc.wantErr, err)
			sent := false
			for _, request := range requests() {
				if request.Method == junieSteerMethod {
					sent = true
					assert.Equal(t, "junie-session", request.Params["sessionId"])
				}
			}
			assert.True(t, sent, "the steer rode the native method")
			assert.False(t, hasMethod(requests(), acp.MethodSessionPrompt), "the steer never rode session/prompt")
		})
	}
}

func hasMethod(requests []agenttest.RecordedRequest, method string) bool {
	for _, request := range requests {
		if request.Method == method {
			return true
		}
	}
	return false
}

// junieSessionUpdateParams builds the params of one session/update, with the
// `_meta` the update carries when it holds one.
func junieSessionUpdateParams(sessionID, sessionUpdate, text string, meta map[string]any) json.RawMessage {
	params := map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": sessionUpdate,
			"content":       map[string]any{"type": "text", "text": text},
		},
	}
	if meta != nil {
		params["_meta"] = meta
	}
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return raw
}

// A steer whose reply says `startedNewTurn` begins a turn of Junie's own that
// waits for the running prompt, and the empty `_meta.lastRequestComplete`
// chunk that ends the turn releases it with no turn-end row of its own.
// Frames from .tmp/probe/junie/run2.
func TestJunieStartedNewTurnEndsAtTheLastRequestCompleteMarker(t *testing.T) {
	t.Parallel()
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == junieSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.HooksForTest().SessionNotificationMetadata = a.observeJunieTurnEnd
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	assert.False(t, a.AgentTurnActive(), "the turn of Junie's own waits for the prompt that runs")

	// The prompt's response hands the busy state to the waiting turn.
	a.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })
	assert.True(t, a.PromptActive(), "the turn of Junie's own holds the busy state")

	a.HandleSessionUpdateForTest(junieSessionUpdateParams("session-1", "agent_message_chunk", "STEERED-ANSWER", nil))
	a.HandleSessionUpdateForTest(junieSessionUpdateParams("session-1", "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	assert.False(t, a.AgentTurnActive(), "the marker chunk ended the turn")
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	steered := false
	turnEnds := 0
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			turnEnds++
			continue
		}
		var frame struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &frame) == nil &&
			frame.Kind == contracts.AssembledMessageKindText && frame.Text == "STEERED-ANSWER" {
			steered = true
		}
	}
	assert.True(t, steered, "the steered answer is stored as its own completed text")
	assert.Equal(t, 1, turnEnds, "the prompt's divider is the only turn end: the marker stores no row")
}
