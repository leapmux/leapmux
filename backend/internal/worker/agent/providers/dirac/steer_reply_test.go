package dirac

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func newDiracSteerReplyAgent(t *testing.T) (*Agent, *agenttest.ControlSink, chan agenttest.RPCReply) {
	t.Helper()
	promptReply := make(chan agenttest.RPCReply, 1)
	a, _ := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(req agenttest.RecordedRequest) agenttest.RPCReply {
			if req.Method == acp.MethodSessionPrompt {
				return <-promptReply
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = a.configure(nil)
	a.SetSessionIDForTest("dirac-session")
	a.SetSteerMethodForTest(diracSteerMethod)
	return a, sink, promptReply
}

func diracSteeringStatusFrame(t *testing.T, sessionID, messageID, status string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "dev.dirac/steering_status",
		"params": map[string]any{
			"sessionId":         sessionID,
			"steeringMessageId": messageID,
			"status":            status,
		},
	})
	require.NoError(t, err)
	return raw
}

func TestDiracSteerBindsExactReplyAtSentStatusAndDropsUnreadOnPromptEnd(t *testing.T) {
	t.Parallel()
	a, sink, promptReply := newDiracSteerReplyAgent(t)

	reporter, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok, "Dirac must report steer resolution at native echo")
	assert.True(t, reporter.SteerRowAtEcho())

	lead, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, lead.TurnID)

	a.HandleOutput(diracFrame(t, diracText(t, "Before whisper.")))

	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Include changelog.",
	}))
	a.HandleOutput(diracSteeringStatusFrame(t, "dirac-session", "transcript-1", "queued"))
	assert.Empty(t, sink.SteerResolutions(), "queued status must not prematurely echo the steer")

	a.HandleOutput(diracSteeringStatusFrame(t, "dirac-session", "transcript-1", "sent"))
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: "dirac-session",
			InputID:   "transcript-1",
			TurnID:    lead.TurnID,
		},
	}, sink.SteerResolutions()[0])

	// Queue a second whisper that is never injected before the turn finishes.
	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Content:   "Never injected.",
	}))
	a.HandleOutput(diracSteeringStatusFrame(t, "dirac-session", "transcript-2", "queued"))
	a.HandleOutput(diracFrame(t, diracText(t, "After whisper.")))

	promptReply <- agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	testutil.RequireEventually(t, func() bool {
		return len(sink.SteerResolutions()) == 2
	})
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[1])
}

func TestDiracSteerClearSessionMarksPendingUncertain(t *testing.T) {
	t.Parallel()
	a, sink, promptReply := newDiracSteerReplyAgent(t)
	defer func() {
		promptReply <- agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}
	}()

	_, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Pending during reset.",
	}))
	a.HooksForTest().ClearProviderState()

	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Outcome:   agent.SteerOutcomeUncertain,
	}, sink.SteerResolutions()[0])
}
