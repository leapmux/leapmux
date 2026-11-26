package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func openCodeInputReplyFixture(t *testing.T, messages func(query url.Values, inputID string) string) (*Agent, *agenttest.ControlSink, agent.NativeInputReceipt) {
	t.Helper()
	var submitted atomic.Value
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_1/prompt":
			var prompt openCodePrompt
			assert.NoError(t, json.NewDecoder(request.Body).Decode(&prompt))
			submitted.Store(prompt.ID)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, prompt.ID)
		case "/api/session/ses_1/message":
			_, _ = fmt.Fprint(w, messages(request.URL.Query(), submitted.Load().(string)))
		case "/api/session/ses_1":
			_, _ = fmt.Fprint(w, `{"data":{"id":"ses_1","agent":"build"}}`)
		case "/api/session/active":
			_, _ = fmt.Fprint(w, `{"data":{}}`)
		case "/api/session/ses_1/permission", "/api/session/ses_1/form":
			_, _ = fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected OpenCode request %s %s", request.Method, request.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	receipt, err := a.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	require.Equal(t, "ses_1", receipt.SessionID)
	require.Equal(t, submitted.Load(), receipt.InputID)
	require.NotEmpty(t, receipt.InputID)
	return a, sink, receipt
}

func TestOpenCodeInputReplyUsesTheExactProjectedExecution(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "16", query.Get("limit"))
		if query.Get("cursor") == "" {
			assert.Equal(t, "desc", query.Get("order"))
			return `{"data":[
			{"id":"msg_future","type":"assistant","content":[{"type":"text","text":"a newer reply"}],"time":{"completed":12}},
			{"id":"msg_reply_end","type":"idle","outcome":"succeeded"}
			],"cursor":{"next":"older-messages"}}`
		}
		assert.Equal(t, "older-messages", query.Get("cursor"))
		assert.Empty(t, query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_answer","type":"assistant","content":[{"type":"text","text":"the exact reply"}],"time":{"completed":8}},
			{"id":%q,"type":"user"},
			{"id":"msg_previous_end","type":"idle","outcome":"succeeded"},
			{"id":"msg_previous","type":"assistant","content":[{"type":"text","text":"an old reply"}],"time":{"completed":2}}
		],"cursor":{}}`, inputID)
	})
	assert.Empty(t, sink.NativeReplyTurns(), "inbox admission alone is not a reply")

	end := []byte(`{"id":"evt_reply_end","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)
	require.NoError(t, a.acceptNativeEvent(t.Context(), end))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	turn := turns[0]
	assert.Equal(t, receipt.SessionID, turn.SessionID)
	assert.Equal(t, "msg_reply_end", turn.TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turn.Outcome)
	require.Len(t, turn.Inputs, 1)
	assert.Equal(t, receipt.InputID, turn.Inputs[0].InputID)
	require.Len(t, turn.Items, 1)
	assert.Equal(t, "opencode:msg_answer:text:0", turn.Items[0].MessageKey)
	assert.Greater(t, turn.Items[0].Ordinal, turn.Inputs[0].AfterOrdinal)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, turn.Items[0].MessageKey, rows[0].IdempotencyKey)
	assert.Contains(t, string(rows[0].Content), "the exact reply")
	assert.True(t, rows[1].TurnEnd)
}

func TestOpenCodeInputReplyRecoversItsNativeEndingAfterReconnect(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "asc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":%q,"type":"user"},
			{"id":"msg_answer","type":"assistant","content":[{"type":"text","text":"recovered reply"}],"time":{"completed":3}},
			{"id":"msg_done","type":"idle","outcome":"succeeded"},
			{"id":"msg_other_input","type":"user"},
			{"id":"msg_other","type":"assistant","content":[{"type":"text","text":"new answer"}],"time":{"completed":7}},
			{"id":"msg_other_done","type":"idle","outcome":"succeeded"}
		],"cursor":{}}`, inputID)
	})

	require.NoError(t, a.restoreRun(t.Context(), a.root))
	require.NoError(t, a.restoreRun(t.Context(), a.root))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, "msg_done", turns[0].TurnID)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "opencode:msg_answer:text:0"}}, turns[0].Items)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestOpenCodeInputReplyKeepsEachBoundaryInTheSharedExecution(t *testing.T) {
	t.Parallel()
	for _, restore := range []bool{false, true} {
		mode := "execution end event"
		if restore {
			mode = "projected recovery without steer echo event"
		}
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var leadID, steerID string
			a, sink, lead := openCodeInputReplyFixture(t, func(query url.Values, submittedID string) string {
				steerID = submittedID
				lead := fmt.Sprintf(`{"id":%q,"type":"user"}`, leadID)
				steer := fmt.Sprintf(`{"id":%q,"type":"user"}`, submittedID)
				const first = `{"id":"msg_first","type":"assistant","content":[{"type":"text","text":"first reply"}],"time":{"completed":2}}`
				const second = `{"id":"msg_second","type":"assistant","content":[{"type":"text","text":"second reply"}],"time":{"completed":7}}`
				const ending = `{"id":"msg_done","type":"idle","outcome":"succeeded"}`
				if query.Get("order") == "asc" {
					return fmt.Sprintf(`{"data":[%s,%s,%s,%s,%s],"cursor":{}}`, lead, first, steer, second, ending)
				}
				assert.Equal(t, "desc", query.Get("order"))
				return fmt.Sprintf(`{"data":[%s,%s,%s,%s,%s],"cursor":{}}`, ending, second, steer, first, lead)
			})
			leadID = lead.InputID
			request := openCodeTestSteerRequest("queued-steer", "steer prompt")
			steer, err := a.SteerInput(request)
			require.NoError(t, err)
			assert.Zero(t, steer)
			if restore {
				require.NoError(t, a.restoreRun(t.Context(), a.root))
				require.NoError(t, a.restoreRun(t.Context(), a.root))
			} else {
				require.NoError(t, a.acceptNativeEvent(t.Context(), []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)))
			}
			expectedSteerReceipt := agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: steerID}
			assert.Equal(t, []agent.SteerResolution{{InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: expectedSteerReceipt}}, sink.SteerResolutions())
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: lead.SessionID, TurnID: "msg_done", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs: []agent.NativeReplyBoundary{{InputID: lead.InputID, AfterOrdinal: 1}, {InputID: steerID, AfterOrdinal: 2}},
				Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "opencode:msg_first:text:0"}, {Ordinal: 3, MessageKey: "opencode:msg_second:text:0"}},
			}}, sink.NativeReplyTurns())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

func TestOpenCodeInputReplyDoesNotUseTheNextInputsExecutionResult(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "desc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_done","type":"idle","outcome":"succeeded"},
			{"id":"msg_other_answer","type":"assistant","content":[{"type":"text","text":"other input's reply"}],"time":{"completed":7}},
			{"id":"msg_other_input","type":"user"},
			{"id":%q,"type":"user"}
		],"cursor":{}}`, inputID)
	})
	end := []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)
	require.NoError(t, a.acceptNativeEvent(t.Context(), end))
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

type openCodePublishedControlSink struct {
	agent.ProviderServices
	published []agent.ControlRequest
}

func (sink *openCodePublishedControlSink) PublishControlRequest(request agent.ControlRequest) error {
	sink.published = append(sink.published, request)
	return sink.ProviderServices.PublishControlRequest(request)
}

func TestOpenCodeInputReplyBindsOnlyControlsFromItsOwnAssistant(t *testing.T) {
	t.Parallel()
	a, _, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "desc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_owned","type":"assistant"},
			{"id":%q,"type":"user"},
			{"id":"msg_prior","type":"assistant"},
			{"id":"msg_prior_input","type":"user"}
		],"cursor":{}}`, inputID)
	})
	controlSink := &openCodePublishedControlSink{ProviderServices: a.root.sink}
	a.root.sink = controlSink

	for i, messageID := range []string{"msg_prior", "msg_owned"} {
		event := assemblyEvent(t, "permission.asked", map[string]any{
			"sessionID": receipt.SessionID, "id": fmt.Sprintf("per_%d", i), "action": "shell", "resources": []string{},
			"source": map[string]any{"type": "tool", "messageID": messageID, "id": "call"},
		})
		require.NoError(t, a.acceptNativeEvent(t.Context(), event))
	}
	require.Len(t, controlSink.published, 2)
	assert.Empty(t, controlSink.published[0].NativeInputIDs)
	assert.Equal(t, []string{receipt.InputID}, controlSink.published[1].NativeInputIDs)
}

func TestOpenCodeSendAndSteerInputForSessionReturnReceipts(t *testing.T) {
	t.Parallel()
	var submittedIDs []string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_1/prompt":
			var prompt openCodePrompt
			assert.NoError(t, json.NewDecoder(request.Body).Decode(&prompt))
			submittedIDs = append(submittedIDs, prompt.ID)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, prompt.ID)
		case "/api/session/ses_1/message":
			lead := fmt.Sprintf(`{"id":%q,"type":"user"}`, submittedIDs[0])
			steer := fmt.Sprintf(`{"id":%q,"type":"user"}`, submittedIDs[1])
			const first = `{"id":"msg_first","type":"assistant","content":[{"type":"text","text":"lead reply"}],"time":{"completed":2}}`
			const second = `{"id":"msg_second","type":"assistant","content":[{"type":"text","text":"steer reply"}],"time":{"completed":7}}`
			const ending = `{"id":"msg_done","type":"idle","outcome":"succeeded"}`
			_, _ = fmt.Fprintf(w, `{"data":[%s,%s,%s,%s,%s],"cursor":{}}`, ending, second, steer, first, lead)
		case "/api/session/ses_1":
			_, _ = fmt.Fprint(w, `{"data":{"id":"ses_1","agent":"build"}}`)
		case "/api/session/active":
			_, _ = fmt.Fprint(w, `{"data":{}}`)
		case "/api/session/ses_1/permission", "/api/session/ses_1/form":
			_, _ = fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected OpenCode request %s %s", request.Method, request.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)

	badSend, err := a.SendInputForSession("ses_wrong", "rejected", nil)
	require.ErrorIs(t, err, agent.ErrInputSessionChanged)
	assert.Empty(t, badSend)

	lead, err := a.SendInputForSession("ses_1", "session lead", nil)
	require.NoError(t, err)
	require.Equal(t, "ses_1", lead.SessionID)
	require.NotEmpty(t, lead.InputID)

	badSteer, err := a.SteerInputForSession("ses_wrong", openCodeTestSteerRequest("queued-bad", "rejected steer"))
	require.ErrorIs(t, err, agent.ErrInputSessionChanged)
	assert.Empty(t, badSteer)

	request := openCodeTestSteerRequest("queued-steer", "session steer")
	steer, err := a.SteerInputForSession("ses_1", request)
	require.NoError(t, err)
	assert.Zero(t, steer)
	require.Len(t, submittedIDs, 2)
	steerID := submittedIDs[1]
	require.NotEqual(t, lead.InputID, steerID)

	require.NoError(t, a.acceptNativeEvent(t.Context(), []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)))
	assert.Equal(t, []agent.SteerResolution{{
		InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{SessionID: "ses_1", InputID: steerID},
	}}, sink.SteerResolutions())
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "ses_1", TurnID: "msg_done", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: lead.InputID, AfterOrdinal: 1}, {InputID: steerID, AfterOrdinal: 2}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "opencode:msg_first:text:0"}, {Ordinal: 3, MessageKey: "opencode:msg_second:text:0"}},
	}}, sink.NativeReplyTurns())
}

func TestOpenCodeSteerInputReturnsEmptyReceiptAndAllowsEmptyAttemptID(t *testing.T) {
	t.Parallel()
	var submitted []string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_1/prompt":
			var prompt openCodePrompt
			assert.NoError(t, json.NewDecoder(request.Body).Decode(&prompt))
			submitted = append(submitted, prompt.ID)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, prompt.ID)
		case "/api/session/ses_1":
			_, _ = fmt.Fprint(w, `{"data":{"id":"ses_1","agent":"build"}}`)
		default:
			t.Errorf("unexpected OpenCode request %s %s", request.Method, request.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, _ := assemblyAgent(t, rpc)
	_, err := a.SendInput("lead", nil)
	require.NoError(t, err)

	// SteerRowAtEcho() is true, so SteerInput and SteerInputForSession must return an empty receipt.
	receipt, err := a.SteerInput(openCodeTestSteerRequest("queued-1", "steer 1"))
	require.NoError(t, err)
	assert.Zero(t, receipt, "SteerInput must not return a confirmed receipt when SteerRowAtEcho is true")

	receiptForSession, err := a.SteerInputForSession("ses_1", openCodeTestSteerRequest("queued-2", "steer 2"))
	require.NoError(t, err)
	assert.Zero(t, receiptForSession, "SteerInputForSession must not return a confirmed receipt when SteerRowAtEcho is true")

	// Control-reply steers carry an empty AttemptID and must still be accepted without tracking steers or replyInputs.
	beforeReplies := len(a.root.pendingInputReplies())
	controlReplyReceipt, err := a.SteerInput(agent.SteerRequest{InputID: "control-reply-1", Content: "feedback text"})
	require.NoError(t, err)
	assert.Zero(t, controlReplyReceipt)
	assert.Len(t, submitted, 4)
	assert.Equal(t, beforeReplies, len(a.root.pendingInputReplies()), "empty-AttemptID steer must not be tracked in replyInputs")
}

func TestOpenCodeInputReplyRetiresOnShutdownIdleRestoreAndMissingEnding(t *testing.T) {
	t.Parallel()
	t.Run("shutdown interruption retires pending reply inputs", func(t *testing.T) {
		t.Parallel()
		a, sink, receipt := openCodeInputReplyFixture(t, func(_ url.Values, _ string) string {
			return `{"data":[],"cursor":{}}`
		})
		shutdown := []byte(`{"id":"evt_shutdown","created":9,"type":"session.execution.interrupted","data":{"sessionID":"ses_1","reason":"shutdown"}}`)
		require.NoError(t, a.acceptNativeEvent(t.Context(), shutdown))
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		assert.False(t, a.root.hasPendingInputReplies())
	})

	t.Run("idle restore without trailing idle record retires pending reply inputs", func(t *testing.T) {
		t.Parallel()
		a, sink, receipt := openCodeInputReplyFixture(t, func(_ url.Values, inputID string) string {
			return fmt.Sprintf(`{"data":[
				{"id":%q,"type":"user"},
				{"id":"msg_partial","type":"assistant","content":[{"type":"text","text":"partial"}],"time":{"completed":3}}
			],"cursor":{}}`, inputID)
		})
		require.NoError(t, a.restoreRun(t.Context(), a.root))
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		assert.False(t, a.root.hasPendingInputReplies())
	})

	t.Run("missing ending record returns error instead of stranding reply inputs", func(t *testing.T) {
		t.Parallel()
		a, _, _ := openCodeInputReplyFixture(t, func(_ url.Values, inputID string) string {
			return fmt.Sprintf(`{"data":[{"id":%q,"type":"user"}],"cursor":{}}`, inputID)
		})
		_, err := a.root.readReplyProjection(t.Context(), "msg_missing_ending")
		assert.Error(t, err, "missing ending record must return an error so recovery runs instead of stranding replyInputs")
	})
}

type openCodeFailFirstPersistSink struct {
	*agenttest.ControlSink
	failPersist atomic.Bool
}

func (s *openCodeFailFirstPersistSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.failPersist.Swap(false) {
		return errors.New("transient store failure")
	}
	return s.ControlSink.PersistMessage(source, content, span)
}

func TestOpenCodeInputReplyRetainedOutputForgetsInputAndFinishesExecution(t *testing.T) {
	t.Parallel()
	var submitted atomic.Value
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_1/prompt":
			var prompt openCodePrompt
			assert.NoError(t, json.NewDecoder(request.Body).Decode(&prompt))
			submitted.Store(prompt.ID)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, prompt.ID)
		case "/api/session/ses_1/message":
			_, _ = fmt.Fprintf(w, `{"data":[
				{"id":"msg_done","type":"idle","outcome":"succeeded"},
				{"id":"msg_answer","type":"assistant","content":[{"type":"text","text":"exact"}],"time":{"completed":8}},
				{"id":%q,"type":"user"}
			],"cursor":{}}`, submitted.Load().(string))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	baseSink := &agenttest.ControlSink{}
	failingSink := &openCodeFailFirstPersistSink{ControlSink: baseSink}
	a, _ := assemblyAgent(t, rpc)
	a.root.sink = agent.NewOrderedProviderServices(&agent.OutputQueue{}, agent.NewProviderServices(failingSink))
	a.root.text.sink = a.root.sink

	_, err := a.SendInput("prompt", nil)
	require.NoError(t, err)

	// Force OutputQueue into backed-up state so subsequent writes return *PendingOutputError.
	failingSink.failPersist.Store(true)
	_ = a.root.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"seed":1}`), AgentSessionID: "ses_1"}, agent.SpanInfo{})

	end := []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)
	_ = a.acceptNativeEvent(t.Context(), end)
	assert.False(t, a.root.hasPendingInputReplies(), "retained PublishNativeReplyTurn must still forget replyInputs")
	require.NoError(t, a.root.sink.OutputQueue().Flush())
	assert.Len(t, baseSink.NativeReplyTurns(), 1)
}
