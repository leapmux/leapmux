package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every steering provider must supply BOTH methods of InputSteerer.
// SupportsSteering belongs to that interface, so a provider that drops it stops
// satisfying InputSteerer, and Manager.SupportsSteering then answers false with
// no build error. These assertions turn that silent regression into a compile
// error.
var (
	_ InputSteerer = (*ClaudeCodeAgent)(nil)
	_ InputSteerer = (*CodexAgent)(nil)
	_ InputSteerer = (*PiAgent)(nil)
	_ InputSteerer = (*zcodeAgent)(nil)
	_ InputSteerer = (*OpenCodeAgent)(nil)
	_ InputSteerer = (*GooseCLIAgent)(nil)
	_ InputSteerer = (*ReasonixAgent)(nil)
)

// steeringStub is an InputSteerer whose SupportsSteering answer the test
// controls. It embeds idleAgent (manager_startup_concurrency_test.go) for the
// rest of the Agent surface, because manager_test.go's stubProvider is
// `//go:build unix` and this case has no platform behaviour.
type steeringStub struct {
	idleAgent
	supports bool
}

func (s *steeringStub) SteerInput(string, []*leapmuxv1.Attachment) error { return nil }
func (s *steeringStub) SupportsSteering() bool                           { return s.supports }

// Manager.SupportsSteering asks the provider and reports the answer. It never
// assumes true for a provider that implements SteerInput, because the Manager
// publishes this answer to the client, which shows or hides the Steer control.
func TestManagerSupportsSteeringAsksTheProvider(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.mu.Lock()
	m.agents["refuses"] = &steeringStub{}
	m.agents["accepts"] = &steeringStub{supports: true}
	m.agents["opencode"] = &OpenCodeAgent{}
	m.agents["goose"] = &GooseCLIAgent{}
	m.agents["plain"] = idleAgent{}
	m.mu.Unlock()

	assert.False(t, m.SupportsSteering("refuses"),
		"a provider that answers false must not claim the capability")
	assert.True(t, m.SupportsSteering("accepts"))
	assert.True(t, m.SupportsSteering("opencode"),
		"OpenCode steers with a second session/prompt and advertises no steer method")
	assert.False(t, m.SupportsSteering("goose"),
		"Goose steers only through the method that its handshake advertises")
	assert.False(t, m.SupportsSteering("plain"),
		"a provider that does not implement SteerInput cannot steer")
	assert.False(t, m.SupportsSteering("unknown-agent"))
}

// A provider that already runs a turn reports ErrAgentBusy, never
// ErrNoActiveTurn. The two sentinels state opposite conditions, and the queue
// reads them differently: ErrAgentBusy is transient, so the item waits for the
// turn to end, while ErrNoActiveTurn says that steering has no target.
//
// Manager.SendInput turns that refusal into a republish of the turn flag, and
// TestManagerSendInputRepublishesTheTurnARefusalDisproves pins that half. This
// one pins the sentinel, and that every provider can republish on demand --
// PublishTurnActive is the interface method the Manager calls.
func TestSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		send func(t *testing.T, sink ProviderServices) (Agent, error)
	}{
		{
			name: "acpBase",
			send: func(t *testing.T, sink ProviderServices) (Agent, error) {
				agent, requests := newACPAgentForRPC(t,
					func() *OpenCodeAgent { return &OpenCodeAgent{} },
					func(agent *OpenCodeAgent) *acpBase { return &agent.acpBase },
				)
				agent.sink = sink
				agent.wireTurnActive()
				agent.promptActive = true
				err := agent.SendInput("later turn", nil)
				assert.Empty(t, requests(), "a refused send must reach no RPC")
				return agent, err
			},
		},
		{
			name: "codex",
			send: func(t *testing.T, sink ProviderServices) (Agent, error) {
				agent, _, requests := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
					return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
				})
				agent.sink = sink
				agent.threadID = "thread-1"
				agent.turnID = "turn-1"
				err := agent.SendInput("later turn", nil)
				assert.Empty(t, requests(), "a refused send must reach no RPC")
				return agent, err
			},
		},
		{
			name: "claude",
			send: func(t *testing.T, sink ProviderServices) (Agent, error) {
				agent := &ClaudeCodeAgent{turnActive: true, sink: sink}
				return agent, agent.SendInput("later turn", nil)
			},
		},
		{
			name: "pi",
			send: func(t *testing.T, sink ProviderServices) (Agent, error) {
				agent := &PiAgent{
					processBase:       processBase{agentID: "test-agent"},
					currentTurnActive: true,
					sink:              sink,
				}
				return agent, agent.SendInput("later turn", nil)
			},
		},
		{
			name: "zcode",
			send: func(t *testing.T, sink ProviderServices) (Agent, error) {
				stdin := &zcodeRecordedStdin{}
				agent := newZCodeTestAgentWithStdin(t, sink, stdin)
				agent.mu.Lock()
				agent.sessionID = "session-1"
				agent.model = "provider/model"
				agent.turnActive = true
				agent.mu.Unlock()
				return agent, agent.SendInput("later turn", nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			provider, err := tc.send(t, sink)
			assert.ErrorIs(t, err, ErrAgentBusy)
			assert.NotErrorIs(t, err, ErrNoActiveTurn,
				"a busy agent has a turn; the no-active-turn sentinel states the opposite")

			// What Manager.SendInput does with that refusal. Every provider must
			// answer it with the turn the refusal proves is in flight. The LAST
			// value is what matters, not the count: Claude's sendInput already
			// publishes from a defer that also covers its error paths and its
			// steer, so it answers twice and the others answer once.
			provider.PublishTurnActive()
			last, published := sink.LastTurnActive()
			require.True(t, published, "the refused provider republishes on demand")
			assert.True(t, last, "and what it republishes is the turn that caused the refusal")
		})
	}
}

func TestClaudeSteerWritesNextPriority(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	agent := &ClaudeCodeAgent{processBase: processBase{stdin: writer}, turnActive: true}
	require.NoError(t, agent.SteerInput("guide", nil))
	line, err := bufio.NewReader(reader).ReadBytes('\n')
	require.NoError(t, err)
	var input struct {
		Priority string `json:"priority"`
	}
	require.NoError(t, json.Unmarshal(line, &input))
	assert.Equal(t, "next", input.Priority)
}

func TestOpenCodeSteerUsesConcurrentACPPrompt(t *testing.T) {
	t.Parallel()

	agent, requests := newACPAgentForRPC(t,
		func() *OpenCodeAgent { return &OpenCodeAgent{} },
		func(agent *OpenCodeAgent) *acpBase { return &agent.acpBase },
	)
	agent.promptActive = true
	require.NoError(t, agent.SteerInput("guide the turn", nil))
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, acpMethodSessionPrompt, requests()[0].Method)
	assert.Equal(t, "session-1", requests()[0].Params["sessionId"])
}

func TestAdvertisedACPSteeringCapability(t *testing.T) {
	t.Parallel()

	goose, gooseRequests := newACPAgentForRPC(t,
		func() *GooseCLIAgent { return &GooseCLIAgent{} },
		func(agent *GooseCLIAgent) *acpBase { return &agent.acpBase },
	)
	assert.False(t, goose.SupportsSteering())
	goose.steerMethod = "_goose/unstable/session/steer"
	goose.promptActive = true
	goose.steerRunID = "run-1"
	assert.True(t, goose.SupportsSteering())
	require.NoError(t, goose.SteerInput("guide", nil))
	require.Len(t, gooseRequests(), 1)
	assert.Equal(t, "_goose/unstable/session/steer", gooseRequests()[0].Method)
	assert.Equal(t, "run-1", gooseRequests()[0].Params["expectedRunId"])

	reasonix, reasonixRequests := newACPAgentForRPC(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(agent *ReasonixAgent) *acpBase { return &agent.acpBase },
	)
	assert.False(t, reasonix.SupportsSteering())
	reasonix.steerMethod = "_reasonix.io/session/steer"
	reasonix.promptActive = true
	require.NoError(t, reasonix.SteerInput("guide", nil))
	require.Len(t, reasonixRequests(), 1)
	assert.Equal(t, "_reasonix.io/session/steer", reasonixRequests()[0].Method)
}

func TestAdvertisedACPSteerMethodDetection(t *testing.T) {
	t.Parallel()

	assert.Equal(t, gooseSteerMethod, parseACPAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`), gooseSteerNamespace, gooseSteerMethod))
	assert.Equal(t, reasonixSteerMethod, parseACPAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"reasonix.io":{"sessionSteer":{"method":"_reasonix.io/session/steer"}}}}}`), reasonixSteerNamespace, reasonixSteerMethod))
	assert.Empty(t, parseACPAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{}}}}`), gooseSteerNamespace, gooseSteerMethod))
	assert.Empty(t, parseACPAdvertisedMethod([]byte(`{"description":"_goose/unstable/session/steer"}`), gooseSteerNamespace, gooseSteerMethod))
	assert.Empty(t, parseACPAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`), reasonixSteerNamespace, reasonixSteerMethod))
}

func TestGooseSessionUpdateTracksActiveRunForSteering(t *testing.T) {
	t.Parallel()

	agent := &GooseCLIAgent{}
	agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":"run-7"}`)})
	assert.Equal(t, "run-7", agent.steerRunID)
	agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":null}`)})
	assert.Empty(t, agent.steerRunID)
}

func TestAdvertisedACPSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	response := func(string) jsonrpcResponsePayload {
		return jsonrpcResponsePayload{Error: json.RawMessage(`{"code":-32602,"message":"session has no active prompt"}`)}
	}
	reasonix, _ := newACPAgentForRPCWithResponder(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(agent *ReasonixAgent) *acpBase { return &agent.acpBase },
		response,
	)
	reasonix.steerMethod = "_reasonix.io/session/steer"
	reasonix.promptActive = true
	assert.ErrorIs(t, reasonix.SteerInput("guide", nil), ErrNoActiveTurn)

	goose, _ := newACPAgentForRPCWithResponder(t,
		func() *GooseCLIAgent { return &GooseCLIAgent{} },
		func(agent *GooseCLIAgent) *acpBase { return &agent.acpBase },
		response,
	)
	goose.steerMethod = "_goose/unstable/session/steer"
	goose.promptActive = true
	goose.steerRunID = "run-1"
	assert.ErrorIs(t, goose.SteerInput("guide", nil), ErrNoActiveTurn)
}

func TestAdvertisedACPSteerTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	reasonix, _ := newACPAgentForRPCWithResponder(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(agent *ReasonixAgent) *acpBase { return &agent.acpBase },
		func(string) jsonrpcResponsePayload {
			<-release
			return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
		},
	)
	reasonix.steerMethod = "_reasonix.io/session/steer"
	reasonix.promptActive = true
	reasonix.apiTimeout = 10 * time.Millisecond

	assert.ErrorIs(t, reasonix.SteerInput("guide", nil), ErrDeliveryUncertain)
}

func TestUnsupportedACPProvidersDoNotImplementSteering(t *testing.T) {
	t.Parallel()

	for provider, candidate := range map[string]any{
		"Kilo":    &KiloAgent{},
		"Copilot": &CopilotCLIAgent{},
		"Cursor":  &CursorCLIAgent{},
	} {
		_, supports := candidate.(InputSteerer)
		assert.False(t, supports, provider)
	}
}

func TestCodexSteerUsesExpectedActiveTurn(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload { return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)} })
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	require.NoError(t, agent.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "turn/steer", requests()[0].Method)
	assert.Equal(t, "turn-1", requests()[0].Params["expectedTurnId"])
}

func TestCodexSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		return jsonrpcResponsePayload{Error: json.RawMessage(`{"code":-32602,"message":"turn is no longer active"}`)}
	})
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	assert.ErrorIs(t, agent.SteerInput("guide", nil), ErrNoActiveTurn)
}

func TestCodexSteerProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	agent, _, _ := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		<-release
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	close(agent.processDone)

	assert.ErrorIs(t, agent.SteerInput("guide", nil), ErrDeliveryUncertain)
}

func TestZCodeSteerRequestsGuideDelivery(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	agent.mu.Lock()
	agent.sessionID = "session-1"
	agent.model = "provider/model"
	agent.turnActive = true
	agent.mu.Unlock()
	answerZCodeRequest(t, agent, stdin, ZCodeMethodSessionSend, `{"accepted":true}`)
	require.NoError(t, agent.SteerInput("guide", nil))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "guide", params["requestedDelivery"])
}

func TestZCodeSteerTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	agent.mu.Lock()
	agent.sessionID = "session-1"
	agent.model = "provider/model"
	agent.turnActive = true
	agent.apiTimeout = 10 * time.Millisecond
	agent.mu.Unlock()

	assert.ErrorIs(t, agent.SteerInput("guide", nil), ErrDeliveryUncertain)
}

// busyProvider refuses every send the way a provider inside its own turn does,
// and counts the republishes the Manager asks for. It embeds idleAgent for the
// rest of the Agent surface, for the same reason steeringStub does.
type busyProvider struct {
	idleAgent
	refuse    error
	republish int
	turnState TurnState
	supports  bool
}

func (p *busyProvider) SendInput(string, []*leapmuxv1.Attachment) error { return p.refuse }
func (p *busyProvider) SteerInput(string, []*leapmuxv1.Attachment) error {
	return nil
}
func (p *busyProvider) SupportsSteering() bool { return p.supports }
func (p *busyProvider) PublishTurnActive() TurnState {
	p.republish++
	return p.turnState
}

func TestManagerSendInputRepublishesTheTurnARefusalDisproves(t *testing.T) {
	t.Parallel()

	// A busy refusal is PROOF that the Worker's view of the turn was wrong: it
	// dispatched into a turn the provider was already running. Both consumers of
	// the flag -- the activity state and the input queue's dispatch guard -- are
	// wrong at that moment, and this is where they are repaired. Doing it here
	// rather than in each provider is what stops a sixth provider from leaving
	// it out.
	for _, tc := range []struct {
		name          string
		refuse        error
		republish     int
		turnState     TurnState
		supports      bool
		wantSteerable bool
	}{
		{
			name: "busy steering provider", refuse: fmt.Errorf("send: %w", ErrAgentBusy), republish: 1,
			turnState: TurnState{Active: true, Steerable: true}, supports: true, wantSteerable: true,
		},
		{
			name: "busy classified compaction", refuse: fmt.Errorf("send: %w", ErrAgentBusy), republish: 1,
			turnState: TurnState{Active: true}, supports: true,
		},
		{
			name: "busy non-steering provider", refuse: fmt.Errorf("send: %w", ErrAgentBusy), republish: 1,
			turnState: TurnState{Active: true},
		},
		{name: "delivered", refuse: nil, republish: 0},
		{name: "other failure", refuse: errors.New("broken pipe"), republish: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &busyProvider{refuse: tc.refuse, turnState: tc.turnState, supports: tc.supports}
			m := NewManager(nil)
			m.mu.Lock()
			m.agents["agent-1"] = provider
			m.mu.Unlock()

			err := m.SendInput("agent-1", "later turn", nil)
			assert.Equal(t, tc.refuse != nil, err != nil)
			assert.Equal(t, tc.republish, provider.republish,
				"only a busy refusal disproves the Worker's view of the turn")
			if tc.republish > 0 {
				var busyErr *AgentBusyError
				require.ErrorAs(t, err, &busyErr)
				assert.Equal(t, tc.wantSteerable, busyErr.ActiveTurnSteerable)
			}
		})
	}
}
