package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openCodeTestSteerRequest(inputID, content string) agent.SteerRequest {
	return agent.SteerRequest{InputID: inputID, AttemptID: "attempt-" + inputID, Content: content}
}

func steerOpenCodeTestInput(runtime *openCodeSessionRuntime, inputID, content string) error {
	_, err := runtime.steerQueuedInput(nil, openCodeTestSteerRequest(inputID, content))
	return err
}

func inputRuntime(t *testing.T, rpc openCodeRPC, sink *agenttest.Sink) *openCodeSessionRuntime {
	t.Helper()
	sink.UpdateSessionID("ses_1")
	var seq atomic.Uint64
	runtime, err := newOpenCodeSessionRuntime(t.Context(), rpc, openCodeSessionInfo{ID: "ses_1", Agent: "build"}, openCodeCatalogFixture(t), openCodeSessionServices{sink: agent.NewProviderServices(sink), claimTurnSequence: func() uint64 { return seq.Add(1) }, requestRecovery: func() {}})
	require.NoError(t, err)
	return runtime
}

func TestOpenCodeInputDispatchesOrdinaryAndLiveInputsThroughNativeSteerReceipts(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var deliveries []string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, "/api/session/ses_1/prompt", r.URL.Path)
		assert.Equal(t, "text", body.Text)
		mu.Lock()
		deliveries = append(deliveries, body.Delivery)
		mu.Unlock()
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	require.NoError(t, runtime.SendInputForSession("ses_1", "text", nil))
	assert.ErrorIs(t, runtime.SendInput("text", nil), agent.ErrAgentBusy)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "text"))
	mu.Lock()
	assert.Equal(t, []string{"steer", "steer"}, deliveries)
	mu.Unlock()
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, runtime.PublishTurnActive())
}

func TestOpenCodeInputRejectsIdentityAndIdleSteeringBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	assert.Error(t, runtime.SendInputForSession("", "text", nil))
	assert.Error(t, runtime.SendInputForSession("ses_other", "text", nil))
	assert.ErrorIs(t, steerOpenCodeTestInput(runtime, "queue-input", "text"), agent.ErrNoActiveTurn)
	runtime.setNativeActivity(true, true)
	assert.ErrorIs(t, steerOpenCodeTestInput(runtime, "queue-input", "text"), agent.ErrNoActiveTurn)
	assert.Zero(t, calls.Load())
}

func TestOpenCodeInputRefusalDoesNotUndoNewerNativeActivity(t *testing.T) {
	t.Parallel()
	for _, nativeEvent := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeEvent), func(t *testing.T) {
			t.Parallel()
			var runtime *openCodeSessionRuntime
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				if nativeEvent {
					runtime.setNativeActivity(true, false)
				}
				w.WriteHeader(http.StatusConflict)
			})
			runtime = inputRuntime(t, rpc, &agenttest.Sink{})
			require.Error(t, runtime.SendInput("text", nil))
			assert.Equal(t, nativeEvent, runtime.PublishTurnActive().Active)
		})
	}
}

func TestOpenCodeInputCompletionCanPrecedeAdmissionReply(t *testing.T) {
	t.Parallel()
	var runtime *openCodeSessionRuntime
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		runtime.setNativeActivity(false, false)
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	runtime = inputRuntime(t, rpc, &agenttest.Sink{})
	require.NoError(t, runtime.SendInput("text", nil))
	assert.False(t, runtime.PublishTurnActive().Active)
}

func TestOpenCodeInputConcurrentSendsAdmitOnlyOneNormalPrompt(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		calls.Add(1)
		close(entered)
		<-release
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- runtime.SendInput("first", nil) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the first native prompt did not reach the server")
	}
	go func() { second <- runtime.SendInput("second", nil) }()
	once.Do(func() { close(release) })
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the first native prompt did not return")
	}
	select {
	case err := <-second:
		require.ErrorIs(t, err, agent.ErrAgentBusy)
	case <-ctx.Done():
		t.Fatal("the concurrent input did not return")
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestOpenCodeInputReconciliationFailureKeepsDeliveryUncertain(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	require.ErrorIs(t, runtime.SendInput("text", nil), agent.ErrDeliveryUncertain)
	assert.True(t, runtime.PublishTurnActive().Active)
}

func TestOpenCodeInputCancelledContextSendsNothing(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	ctx, cancel := context.WithCancel(t.Context())
	runtime.ctx = ctx
	cancel()
	require.ErrorIs(t, runtime.SendInput("text", nil), context.Canceled)
	assert.Zero(t, calls.Load())
}

func TestOpenCodeSteerRefusalPreservesTheRunningTurn(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	runtime.setNativeActivity(true, false)
	err := steerOpenCodeTestInput(runtime, "queue-input", "steer")
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, runtime.PublishTurnActive())
}

func TestOpenCodeInputForeignReceiptLeavesAdmissionUncertain(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_other","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	require.ErrorIs(t, runtime.SendInput("text", nil), agent.ErrDeliveryUncertain)
	assert.True(t, runtime.PublishTurnActive().Active)
}

func TestOpenCodeInputRetiredSessionSendsAndPublishesNothing(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.claimTurnSequence = func() uint64 { return 0 }
	require.ErrorIs(t, runtime.SendInput("text", nil), agent.ErrInputSessionChanged)
	runtime.setNativeActivity(true, false)
	assert.Equal(t, agent.TurnState{}, runtime.PublishTurnActive())
	assert.Empty(t, sink.TurnActiveCalls)
	assert.Zero(t, calls.Load())
}

func TestOpenCodeInputLateRefusalCannotPublishAfterSessionRetirement(t *testing.T) {
	t.Parallel()
	var current atomic.Bool
	current.Store(true)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { current.Store(false); w.WriteHeader(http.StatusConflict) })
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	var seq atomic.Uint64
	runtime.claimTurnSequence = func() uint64 {
		if !current.Load() {
			return 0
		}
		return seq.Add(1)
	}
	require.Error(t, runtime.SendInput("text", nil))
	assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
}

func TestOpenCodeInputInvalidModelDoesNotPublishActivityOrSendHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.selectedModel = &openCodeModelRef{ProviderID: "missing", ID: "model"}
	require.Error(t, runtime.SendInput("text", nil))
	assert.False(t, runtime.PublishTurnActive().Active)
	assert.Equal(t, []bool{false}, sink.TurnActiveCalls)
	assert.Zero(t, calls.Load())
}

func TestOpenCodeSessionRuntimeRequiresItsConstructionInputs(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	info := openCodeSessionInfo{ID: "ses_1"}
	catalog := openCodeCatalogFixture(t)
	sink := agent.NewProviderServices(&agenttest.Sink{})
	sequence := func() uint64 { return 1 }
	recoverStream := func() {}
	services := openCodeSessionServices{sink: sink, claimTurnSequence: sequence, requestRecovery: recoverStream}
	_, err := newOpenCodeSessionRuntime(nil, rpc, info, catalog, services) //nolint:staticcheck // The constructor must reject an absent context.
	require.Error(t, err)
	_, err = newOpenCodeSessionRuntime(t.Context(), openCodeRPC{}, info, catalog, services)
	require.Error(t, err)
	for _, incomplete := range []openCodeSessionServices{
		{claimTurnSequence: sequence, requestRecovery: recoverStream},
		{sink: sink, requestRecovery: recoverStream},
		{sink: sink, claimTurnSequence: sequence},
	} {
		_, err = newOpenCodeSessionRuntime(t.Context(), rpc, info, catalog, incomplete)
		require.Error(t, err)
	}
	_, err = newOpenCodeSessionRuntime(t.Context(), rpc, openCodeSessionInfo{}, catalog, services)
	require.Error(t, err)
}

func TestOpenCodeInputNativeAdmissionSurvivesLostHTTPReceipt(t *testing.T) {
	t.Parallel()
	var runtime *openCodeSessionRuntime
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_, err := runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.enqueued", `"inboxID":"`+body.ID+`"`))
		assert.NoError(t, err)
		w.WriteHeader(http.StatusInternalServerError)
	})
	runtime = inputRuntime(t, rpc, &agenttest.Sink{})
	require.NoError(t, runtime.SendInput("admitted", nil))
	assert.True(t, runtime.PublishTurnActive().Active)
	assert.Empty(t, runtime.admissions)
}

func TestOpenCodeInputSteerEchoCanPrecedeItsHTTPReceipt(t *testing.T) {
	t.Parallel()
	var runtime *openCodeSessionRuntime
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		event := openCodeSessionEventForTest(t, "inbox.delivered", `"inboxID":"`+body.ID+`"`)
		_, err := runtime.observeInbox(event)
		assert.NoError(t, err)
		_, err = runtime.observeInbox(event)
		assert.NoError(t, err)
		_, err = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{}
	runtime = inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "steer"))
	reports := sink.SteerResolutions()
	require.Len(t, reports, 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, reports[0].Outcome)
	assert.Equal(t, "queue-input", reports[0].InputID)
	assert.NotEmpty(t, reports[0].Receipt.InputID)
	assert.Empty(t, runtime.steers)
}

func TestOpenCodeInputCancelledSteerIsDroppedByItsOwnID(t *testing.T) {
	t.Parallel()
	var idsMu sync.Mutex
	var ids []string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		idsMu.Lock()
		ids = append(ids, body.ID)
		idsMu.Unlock()
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	require.NoError(t, steerOpenCodeTestInput(runtime, "first", "identical"))
	require.NoError(t, steerOpenCodeTestInput(runtime, "second", "identical"))
	idsMu.Lock()
	retainedIDs := append([]string(nil), ids...)
	idsMu.Unlock()
	require.Len(t, retainedIDs, 2)
	assert.NotEqual(t, retainedIDs[0], retainedIDs[1])
	_, err := runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.cancelled", `"inboxID":"`+retainedIDs[1]+`"`))
	require.NoError(t, err)
	dropped := agent.SteerResolution{InputID: "second", AttemptID: "attempt-second", Outcome: agent.SteerOutcomeDropped}
	assert.Equal(t, []agent.SteerResolution{dropped}, sink.SteerResolutions())
	_, err = runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.delivered", `"inboxID":"`+retainedIDs[0]+`"`))
	require.NoError(t, err)
	assert.Equal(t, []agent.SteerResolution{dropped, {
		InputID: "first", AttemptID: "attempt-first", Outcome: agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{SessionID: "ses_1", InputID: retainedIDs[0]},
	}}, sink.SteerResolutions())
}

func TestOpenCodeInputUnprovedSteersStayUncertainAtProcessExit(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "steer"))
	runtime.finishPendingSteers()
	runtime.finishPendingSteers()
	reports := sink.SteerResolutions()
	require.Len(t, reports, 1)
	assert.Equal(t, agent.SteerOutcomeUncertain, reports[0].Outcome)
}

func TestOpenCodeInputSteerEchoWaitsForPrecedingTextPersistence(t *testing.T) {
	t.Parallel()
	var runtime *openCodeSessionRuntime
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, err := runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.delivered", `"inboxID":"`+body.ID+`"`))
		assert.NoError(t, err)
		_, err = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	runtime = inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	text := newOpenCodeTextStreams("ses_1", runtime.sink)
	_, err := text.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_text","ordinal":0,"text":"before the steer"`))
	require.NoError(t, err)
	_, err = text.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_text"`))
	require.ErrorIs(t, err, assert.AnError)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "steer"))
	assert.Empty(t, sink.SteerResolutions())
	assert.True(t, runtime.sink.OutputQueue().HasPending())
	sink.PersistErr = nil
	require.NoError(t, runtime.sink.OutputQueue().Flush())
	reports := sink.SteerResolutions()
	require.Len(t, reports, 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, reports[0].Outcome)
	rows := sink.Messages()
	require.GreaterOrEqual(t, len(rows), 2)
	for _, row := range rows {
		assert.Equal(t, string(rows[0].Content), string(row.Content))
	}
	assert.Contains(t, string(rows[len(rows)-1].Content), "before the steer")
	assert.False(t, runtime.sink.OutputQueue().HasPending())
}

func TestOpenCodeInputForeignInboxEventCannotResolveAnOwnedSteer(t *testing.T) {
	t.Parallel()
	var submittedMu sync.Mutex
	var submitted string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body openCodePrompt
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		submittedMu.Lock()
		submitted = body.ID
		submittedMu.Unlock()
		_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, body.ID)
		assert.NoError(t, err)
	})
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "steer"))
	submittedMu.Lock()
	inputID := submitted
	submittedMu.Unlock()
	event := openCodeSessionEventForTest(t, "inbox.delivered", `"inboxID":"`+inputID+`"`)
	event.sessionID = "ses_other"
	_, err := runtime.observeInbox(event)
	require.Error(t, err)
	assert.Empty(t, sink.SteerResolutions())
	assert.Len(t, runtime.steers, 1)
}

func TestOpenCodeInputReconcilesUnknownAdmissionWithoutAStreamReconnect(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"absent", "queued", "delivered", "newer activity"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			var posts atomic.Int32
			var submitted string
			var runtime *openCodeSessionRuntime
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/session/ses_1/prompt":
					var request openCodePrompt
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					submitted = request.ID
					if posts.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, request.ID)
				case "/api/session/ses_1/inbox":
					if state == "queued" {
						_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"sessionID":"ses_1","type":"user"}]}`, submitted)
					} else {
						_, _ = fmt.Fprint(w, `{"data":[]}`)
					}
				case "/api/session/ses_1/message":
					if state == "delivered" {
						_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"type":"user"}],"cursor":{}}`, submitted)
					} else {
						_, _ = fmt.Fprint(w, `{"data":[],"cursor":{}}`)
					}
				case "/api/session/active":
					if state == "newer activity" {
						runtime.setNativeActivity(true, false)
					}
					_, _ = fmt.Fprint(w, `{"data":{}}`)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			runtime = inputRuntime(t, rpc, &agenttest.Sink{})
			err := runtime.SendInput("first", nil)
			if state == "queued" || state == "delivered" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			assert.Equal(t, state == "newer activity", runtime.PublishTurnActive().Active)
			assert.EqualValues(t, 1, posts.Load(), "reconciliation must not resubmit an uncertain input")
			if state == "absent" {
				require.NoError(t, runtime.SendInput("second", nil))
				assert.EqualValues(t, 2, posts.Load())
			}
		})
	}
}

func TestOpenCodeInputReconciledSteerRetainsItsEchoTicket(t *testing.T) {
	t.Parallel()
	var submitted string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_1/prompt":
			var request openCodePrompt
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			submitted = request.ID
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/session/ses_1/inbox":
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"sessionID":"ses_1","type":"user"}]}`, submitted)
		case "/api/session/active":
			_, _ = fmt.Fprint(w, `{"data":{"ses_1":{"type":"running"}}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	sink := &agenttest.Sink{}
	runtime := inputRuntime(t, rpc, sink)
	runtime.setNativeActivity(true, false)
	require.NoError(t, steerOpenCodeTestInput(runtime, "queue-input", "steer"))
	_, err := runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.delivered", `"inboxID":"`+submitted+`"`))
	require.NoError(t, err)
	reports := sink.SteerResolutions()
	require.Len(t, reports, 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, reports[0].Outcome)
}

func TestOpenCodeInputActivityCanRecoverWhenAdmissionEvidenceFails(t *testing.T) {
	t.Parallel()
	for _, failed := range []string{"inbox", "message"} {
		t.Run(failed, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost || r.URL.Path == "/api/session/ses_1/"+failed {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				switch r.URL.Path {
				case "/api/session/ses_1/inbox":
					_, _ = fmt.Fprint(w, `{"data":[]}`)
				case "/api/session/ses_1/message":
					_, _ = fmt.Fprint(w, `{"data":[],"cursor":{}}`)
				case "/api/session/active":
					_, _ = fmt.Fprint(w, `{"data":{}}`)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			runtime := inputRuntime(t, rpc, &agenttest.Sink{})
			require.ErrorIs(t, runtime.SendInput("text", nil), agent.ErrDeliveryUncertain)
			assert.False(t, runtime.PublishTurnActive().Active)
		})
	}
}

func TestOpenCodeInputRequestsRecoveryAfterAnUncertainHTTPOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		status    int
		confirmed bool
		want      int32
	}{
		{"success", http.StatusOK, false, 0},
		{"definite refusal", http.StatusBadRequest, false, 0},
		{"unconfirmed server error", http.StatusInternalServerError, false, 1},
		{"confirmed server error", http.StatusInternalServerError, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var runtime *openCodeSessionRuntime
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					switch r.URL.Path {
					case "/api/session/active":
						_, _ = fmt.Fprint(w, `{"data":{}}`)
					case "/api/session/ses_1/message":
						_, _ = fmt.Fprint(w, `{"data":[],"cursor":{}}`)
					default:
						_, _ = fmt.Fprint(w, `{"data":[]}`)
					}
					return
				}
				var input openCodePrompt
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				if tc.confirmed {
					_, err := runtime.observeInbox(openCodeSessionEventForTest(t, "inbox.enqueued", `"inboxID":"`+input.ID+`"`))
					assert.NoError(t, err)
				}
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, input.ID)
				}
			})
			runtime = inputRuntime(t, rpc, &agenttest.Sink{})
			var requested atomic.Int32
			runtime.requestRecovery = func() { requested.Add(1) }
			err := runtime.SendInput("text", nil)
			if tc.status == http.StatusOK || tc.confirmed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, tc.want, requested.Load())
		})
	}
}

func TestOpenCodeRuntimeSettingsObservationRequiresTheOwnedSession(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	require.Error(t, runtime.observeSessionInfo(openCodeSessionInfo{ID: "ses_other", Agent: "plan"}))
	assert.Equal(t, "build", runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDPrimaryAgent])
	require.NoError(t, runtime.observeSessionInfo(openCodeSessionInfo{ID: "ses_1", Agent: "plan", Model: &openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "default"}}))
	assert.Equal(t, agent.EffortAuto, runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDEffort])
}

func TestOpenCodeRuntimeSnapshotCannotChangeTheNativeParent(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	err := runtime.observeSessionInfo(openCodeSessionInfo{ID: "ses_1", ParentID: "ses_foreign", Agent: "plan"})
	require.Error(t, err)
	assert.Equal(t, "build", runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDPrimaryAgent])
}

func TestOpenCodeRuntimeOwnsTheObservedModelSelection(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	model := &openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "high"}
	require.NoError(t, runtime.observeSessionInfo(openCodeSessionInfo{ID: "ses_1", Agent: "build", Model: model}))
	model.Variant = "low"
	assert.Equal(t, "variant:high", runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDEffort])
}
