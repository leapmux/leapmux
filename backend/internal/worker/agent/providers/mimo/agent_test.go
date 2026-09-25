package mimo

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every prompt states the agent, the model and the variant, because MiMo keeps
// no current selection for a session.
func TestSendInputStatesTheSettings(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.effort = "high"

	require.NoError(t, a.SendInput("hello", nil))
	requests := server.requestsTo("POST /session/ses_test/prompt_async")
	require.Len(t, requests, 1)
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"hello"}],"agent":"build","model":{"providerID":"mock","modelID":"alpha"},"variant":"high"}`,
		string(requests[0].Body))

	a.model, a.effort, a.mode = "mock/beta", agent.EffortAuto, contracts.MiMoModePlan
	require.NoError(t, a.SendInput("again", nil))
	requests = server.requestsTo("POST /session/ses_test/prompt_async")
	require.Len(t, requests, 2)
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"again"}],"agent":"plan","model":{"providerID":"mock","modelID":"beta"}}`,
		string(requests[1].Body), "Auto and a model with no variant send no variant")
}

func TestSendInputWithoutAModelLetsTheServerPick(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.model = ""

	require.NoError(t, a.SendInput("hello", nil))
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"hello"}],"agent":"build"}`,
		string(server.requestsTo("POST /session/ses_test/prompt_async")[0].Body))
}

func TestSendInputRefusesWhileATurnRuns(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))

	err := a.SendInput("wait for it", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"), "the queue holds the message for the next turn")
}

func TestSteerInput(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	assert.True(t, a.SupportsSteering())

	assert.ErrorIs(t, a.SteerInput("nothing runs", nil), agent.ErrNoActiveTurn)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	require.NoError(t, a.SteerInput("also check the tests", nil))
	requests := server.requestsTo("POST /session/ses_test/prompt_async")
	require.Len(t, requests, 1, "a prompt that reaches a running loop joins it")
	assert.Equal(t, "also check the tests", decodeBody(t, requests[0])["parts"].([]any)[0].(map[string]any)["text"])
}

func TestSendInputForSession(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)

	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	require.NoError(t, a.SendInputForSession(testSessionID, "for this session", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestSendInputRefusesAnAttachmentItCannotSend(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)

	err := a.SendInput("run this", []*leapmuxv1.Attachment{{Filename: "tool.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}})
	assert.ErrorContains(t, err, "tool.bin")
	assert.Empty(t, server.allRequests())
}

func TestSendInputOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.SetStoppedForTest(true)

	assert.ErrorContains(t, a.SendInput("hello", nil), "stopped")
	assert.ErrorContains(t, a.SendRawInput([]byte(`{}`)), "stopped")
	assert.Empty(t, server.allRequests())
}

func TestSendInputWithoutASession(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.sessionID = ""

	assert.ErrorContains(t, a.SendInput("hello", nil), "no MiMo session")
	assert.Empty(t, server.allRequests())
}

func TestSendInputReportsARefusal(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session/ses_test/prompt_async", http.StatusBadRequest, `{"name":"BadRequest"}`)

	err := a.SendInput("hello", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "a status reply is the server's own answer")
	assert.True(t, providerkit.IsHTTPStatus(err, http.StatusBadRequest))
}

// MiMo limits its prompt route to 20 requests a minute for the whole server,
// and it answers the 21st with 429. The refusal states the limit, so the user
// knows why the message failed and when to send it again. It is no busy
// refusal: no turn end would deliver the message, and an idle agent would
// dispatch it again at once, into the same limit.
func TestSendInputStatesMiMosRateLimit(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Retry-After", "37")
		writeJSON(w, http.StatusTooManyRequests, `{"error":"Too many requests"}`)
	})

	err := a.SendInput("hello", nil)
	require.ErrorIs(t, err, errPromptRateLimited)
	assert.ErrorContains(t, err, "20 messages a minute")
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "a status reply is the server's own answer")
	assert.NotErrorIs(t, err, agent.ErrAgentBusy)
	assert.True(t, providerkit.IsHTTPStatus(err, http.StatusTooManyRequests), "the cause stays in the chain")

	steerAgent, steerServer := newTestAgent(t, nil)
	steerServer.respond("POST /session/ses_test/prompt_async", http.StatusTooManyRequests, `{"error":"Too many requests"}`)
	feed(steerAgent, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.ErrorIs(t, steerAgent.SteerInput("also this", nil), errPromptRateLimited, "a steer uses the same route")
}

// A connection that closes after the server read the request leaves the
// delivery unknown, and the queue must not send the text again as if it never
// arrived.
func TestSendInputReportsAnUncertainDelivery(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		// The handler runs on the server's goroutine, where a failed require
		// cannot stop the test; a failed hijack answers with a status instead,
		// which the assertion below then reports.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	})

	assert.ErrorIs(t, a.SendInput("hello", nil), agent.ErrDeliveryUncertain)
}

func TestClassifyDeliveryError(t *testing.T) {
	t.Parallel()

	status := &providerkit.HTTPStatusError{Method: "POST", Path: "/x", StatusCode: 500, Status: "500 Internal Server Error"}
	assert.NotErrorIs(t, classifyDeliveryError("prompt", status), agent.ErrDeliveryUncertain)
	assert.NotErrorIs(t, classifyDeliveryError("prompt", status), errPromptRateLimited, "only a 429 states the limit")

	limited := &providerkit.HTTPStatusError{Method: "POST", Path: "/x", StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}
	assert.ErrorIs(t, classifyDeliveryError("subagent message", limited), errPromptRateLimited)
	assert.NotErrorIs(t, classifyDeliveryError("subagent message", limited), agent.ErrDeliveryUncertain)

	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	assert.NotErrorIs(t, classifyDeliveryError("prompt", dial), agent.ErrDeliveryUncertain, "a refused dial never sent the request")

	read := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	err := classifyDeliveryError("prompt", read)
	assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.ErrorIs(t, err, read, "the cause stays in the chain")
}

// The repeat guard holds until a prompt reaches the server. A refused prompt
// starts no turn, so MiMo can still repeat the old failure after it.
func TestSendInputClearsTheRepeatGuardOnDelivery(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.lastTurnFailed = true
	server.respond("POST /session/ses_test/prompt_async", http.StatusBadRequest, `{}`)

	require.Error(t, a.SendInput("hello", nil))
	assert.True(t, a.lastTurnFailed)

	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, a.SendInput("hello", nil))
	assert.False(t, a.lastTurnFailed)
}

// The worker's own calls run on goroutines other than the stream's. Each call
// and each handler takes a.Mu for its own steps, so the race detector reports
// a field that one of them reads or writes without the lock. The turns that the
// stream reports stay whole whatever the calls do between their events.
func TestConcurrentCallsAndEvents(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)
	const rounds = 40
	input := map[string]any{"command": "ls"}
	events := [][]byte{messageEvent(t, "msg_1", roleAssistant, mainActorID, false)}
	for i := range rounds {
		callID := fmt.Sprintf("call-%d", i)
		events = append(events,
			statusEvent(t, contracts.MiMoStatusTypeBusy),
			toolPartEvent(t, "prt_"+callID, "msg_1", contracts.MiMoToolBash, callID, toolState{Status: contracts.MiMoToolStatusRunning, Input: input}),
			toolPartEvent(t, "prt_"+callID, "msg_1", contracts.MiMoToolBash, callID, toolState{Status: contracts.MiMoToolStatusCompleted,
				Input: input, Output: "ok"}),
			statusEvent(t, contracts.MiMoStatusTypeIdle),
		)
	}
	efforts := []string{"high", "low"}

	var wg sync.WaitGroup
	wg.Go(func() { feed(a, events...) })
	wg.Go(func() {
		for range rounds {
			_ = a.SendInput("hello", nil)
			_ = a.SteerInput("also this", nil)
		}
	})
	wg.Go(func() {
		for i := range rounds {
			a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: efforts[i%len(efforts)]})
			_ = a.OptionGroups()
		}
	})
	wg.Go(func() {
		for range rounds {
			_ = a.SupportedGoalActions()
			_ = a.PublishTurnActive()
			_ = a.usageSnapshot()
		}
	})
	wg.Wait()

	turnEnds, closers := 0, 0
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			turnEnds++
		}
		if message.Closing {
			closers++
		}
	}
	assert.Equal(t, rounds, turnEnds, "each busy and idle pair is one turn")
	assert.Equal(t, rounds, closers, "each call closes once")
	assert.False(t, a.turnActive)
	assert.Contains(t, efforts, a.effort, "the last settings update stays whole")
}

func TestSendRawInputRefusesAnUnknownRequest(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	assert.ErrorContains(t, a.SendRawInput(allowEnvelope("mimo-permission:per_9")), "no pending request")
	assert.Empty(t, server.allRequests())
}
