package dirac

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, lead.TurnID)

	a.HandleOutput(diracFrame(t, diracText(t, "Before whisper.")))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
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
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
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

	_, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
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

func TestDiracSteerAttachmentOnlyReturnsUnsupported(t *testing.T) {
	t.Parallel()
	a, _, _ := newDiracSteerReplyAgent(t)
	a.SetPromptActiveForTest(true)

	_, err := a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-att",
		AttemptID: "attempt-att",
		Content:   "",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "shot.png", MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	})
	assert.ErrorIs(t, err, agent.ErrSteeringUnsupported)
}

func TestDiracSteerRejectedStatusDropsImmediately(t *testing.T) {
	t.Parallel()
	a, sink, promptReply := newDiracSteerReplyAgent(t)
	defer func() {
		promptReply <- agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	}()

	_, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-reject",
		AttemptID: "attempt-reject",
		Content:   "Rejected whisper.",
	}))
	a.HandleOutput(diracSteeringStatusFrame(t, "dirac-session", "transcript-rej", "dropped"))

	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-reject",
		AttemptID: "attempt-reject",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[0])
}

func TestDiracSteerAlreadyEchoedBeforeTurnEndDoesNotReturnErrNoActiveTurn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe := io.Pipe()
	a := &Agent{}
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	onWrite := &diracHookWriter{
		w: writePipe,
		onLine: func(line []byte) {
			var req struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &req) == nil && req.Method == diracSteerMethod {
				// Simulate Dirac echoing the whisper and ending the turn before SendNotification returns.
				a.HandleOutput(diracSteeringStatusFrame(t, "dirac-session", "transcript-echo", "sent"))
				a.SetPromptActiveForTest(false)
			}
		},
	}
	a.AttachPeerForTest(ctx, cancel, onWrite, "test-agent", "dirac-session")
	*a.HooksForTest() = a.configure(nil)
	a.SetSteerMethodForTest(diracSteerMethod)

	promptIDCh := make(chan int64, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		scanner := bufio.NewScanner(readPipe)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			var id int64
			if json.Unmarshal(req.ID, &id) != nil {
				continue
			}
			if req.Method == acp.MethodSessionPrompt {
				promptIDCh <- id
			}
		}
	})
	t.Cleanup(func() {
		cancel()
		_ = writePipe.Close()
		_ = readPipe.Close()
		wg.Wait()
	})

	_, err := a.SendInput("Lead prompt.", nil)
	require.NoError(t, err)
	promptID := <-promptIDCh
	defer a.Deliver(promptID, agenttest.JSONRPCResponse(promptID, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}))

	_, err = a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-echoed",
		AttemptID: "attempt-echoed",
		Content:   "Already echoed.",
	})
	require.NoError(t, err)
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, sink.SteerResolutions()[0].Outcome)
}

type diracHookWriter struct {
	w      io.WriteCloser
	onLine func([]byte)
}

func (h *diracHookWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	if err == nil && h.onLine != nil {
		h.onLine(p)
	}
	return n, err
}

func (h *diracHookWriter) Close() error {
	return h.w.Close()
}
