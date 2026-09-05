package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	assert.Equal(t, "_goose/unstable/session/steer", advertisedACPSteerMethod("goose", []byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`)))
	assert.Equal(t, "_reasonix.io/session/steer", advertisedACPSteerMethod("reasonix", []byte(`{"agentCapabilities":{"_meta":{"reasonix.io":{"sessionSteer":{"method":"_reasonix.io/session/steer"}}}}}`)))
	assert.Empty(t, advertisedACPSteerMethod("goose", []byte(`{"agentCapabilities":{"_meta":{"goose":{}}}}`)))
	assert.Empty(t, advertisedACPSteerMethod("goose", []byte(`{"description":"_goose/unstable/session/steer"}`)))
	assert.Empty(t, advertisedACPSteerMethod("opencode", []byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`)))
}

func TestGooseSessionUpdateTracksActiveRunForSteering(t *testing.T) {
	t.Parallel()

	agent := &GooseCLIAgent{}
	agent.providerName = "goose"
	agent.captureGooseSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":"run-7"}`)})
	assert.Equal(t, "run-7", agent.steerRunID)
	agent.captureGooseSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":null}`)})
	assert.Empty(t, agent.steerRunID)
}

func TestAdvertisedACPSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	response := func(string) json.RawMessage {
		return json.RawMessage(`{"code":-32602,"message":"session has no active prompt"}`)
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
		func(string) json.RawMessage {
			<-release
			return json.RawMessage(`{}`)
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

	agent, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	require.NoError(t, agent.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "turn/steer", requests()[0].Method)
	assert.Equal(t, "turn-1", requests()[0].Params["expectedTurnId"])
}

func TestCodexSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{"code":-32602,"message":"turn is no longer active"}`)
	})
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	assert.ErrorIs(t, agent.SteerInput("guide", nil), ErrNoActiveTurn)
}

func TestCodexSteerProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		<-release
		return json.RawMessage(`{}`)
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
