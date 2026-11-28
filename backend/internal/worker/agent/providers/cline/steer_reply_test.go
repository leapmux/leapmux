package cline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const steerTestClient = "leapmux-client"
const steerTestSession = "session-1"

func testSteerDispatch(requestID, queueID, prompt string) *steerDispatch {
	return &steerDispatch{
		input: clineInput{prompt: prompt},
		resolution: agent.SteerResolution{
			InputID: queueID, AttemptID: "attempt-" + queueID,
			Receipt: agent.NativeInputReceipt{SessionID: steerTestSession, InputID: requestID},
		},
	}
}

func feedSteerProof(t *testing.T, proofs *steerProofs, sequence int64, name string, payload any) steerObservation {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	event := hubEvent{Event: name, SessionID: steerTestSession, Sequence: sequence, Payload: raw}
	return proofs.observe(event, decodeSteerEvent(event), steerTestClient)
}

func pendingPrompt(id, prompt string) nativePendingPrompt {
	return nativePendingPrompt{ID: id, Prompt: prompt, Delivery: deliverySteer}
}

func registerSteerStart(t *testing.T, proofs *steerProofs, sequence int64, dispatch *steerDispatch) {
	t.Helper()
	proofs.register(dispatch)
	started := feedSteerProof(t, proofs, sequence, eventRunStarted, map[string]string{
		"requestId": dispatch.resolution.Receipt.InputID, "clientId": steerTestClient,
	})
	require.True(t, started.ownStart)
	require.Empty(t, started.resolutions)
}

func testQueue(t *testing.T, proofs *steerProofs, sequence int64, prompts ...nativePendingPrompt) steerObservation {
	t.Helper()
	return feedSteerProof(t, proofs, sequence, eventPendingPrompts, map[string]any{"prompts": append([]nativePendingPrompt{}, prompts...)})
}

func TestSteerProofsRequireBothNativeEventPairs(t *testing.T) {
	t.Parallel()
	var proofs steerProofs
	request := testSteerDispatch("request", "input", "The revised prompt.")
	admissionDone := make(chan struct{})
	request.admissionDone = admissionDone
	queued := pendingPrompt("native-pending", "The revised prompt.")
	registerSteerStart(t, &proofs, 7, request)
	require.Empty(t, testQueue(t, &proofs, 8, queued).resolutions)
	assert.True(t, request.admitted)
	select {
	case <-admissionDone:
	default:
		t.Fatal("the native queue admission did not release the caller")
	}
	assert.Empty(t, feedSteerProof(t, &proofs, 9, eventIterationStarted, nil).resolutions)
	// The queue adds another draft without changing the registered entry.
	foreign := pendingPrompt("foreign-pending", "Another draft.")
	assert.Empty(t, testQueue(t, &proofs, 10, foreign, queued).resolutions)
	require.Empty(t, testQueue(t, &proofs, 11, foreign).resolutions)
	got := feedSteerProof(t, &proofs, 12, eventPendingPromptSubmitted, map[string]any{"prompt": queued})
	want := request.resolution
	want.Outcome = agent.SteerOutcomeEchoed
	assert.Equal(t, []agent.SteerResolution{want}, got.resolutions)
	assert.Empty(t, proofs.reset())
}

func TestSteerProofsRejectUnprovedQueueMutations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		feed func(*testing.T, *steerProofs, nativePendingPrompt) steerObservation
	}{
		{"a gap before queue admission", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			return testQueue(t, proofs, 9, prompt)
		}},
		{"a different attachment at admission", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			prompt.UserFiles = []string{"other-file"}
			return testQueue(t, proofs, 8, prompt)
		}},
		{"an unpaired merge of identical prompts", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			require.Empty(t, testQueue(t, proofs, 8, prompt).resolutions)
			return testQueue(t, proofs, 9, prompt)
		}},
		{"an event gap while the input is queued", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			require.Empty(t, testQueue(t, proofs, 8, prompt).resolutions)
			return feedSteerProof(t, proofs, 10, eventIterationStarted, nil)
		}},
		{"a queue deletion without a submitted event", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			require.Empty(t, testQueue(t, proofs, 8, prompt).resolutions)
			require.Empty(t, testQueue(t, proofs, 9).resolutions)
			return feedSteerProof(t, proofs, 10, eventIterationStarted, nil)
		}},
		{"submission of another native ID", func(t *testing.T, proofs *steerProofs, prompt nativePendingPrompt) steerObservation {
			require.Empty(t, testQueue(t, proofs, 8, prompt).resolutions)
			require.Empty(t, testQueue(t, proofs, 9).resolutions)
			prompt.ID = "another-native-id"
			return feedSteerProof(t, proofs, 10, eventPendingPromptSubmitted, map[string]any{"prompt": prompt})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var proofs steerProofs
			request := testSteerDispatch("request", "input", "The revised prompt.")
			registerSteerStart(t, &proofs, 7, request)
			got := tc.feed(t, &proofs, pendingPrompt("native-pending", request.input.prompt))
			assert.Equal(t, []agent.SteerResolution{{InputID: "input", AttemptID: "attempt-input", Outcome: agent.SteerOutcomeUncertain}}, got.resolutions)
			assert.Empty(t, proofs.reset())
		})
	}
}

func TestSteerProofsGiveACoalescedIDToTheLatestProvedCommand(t *testing.T) {
	t.Parallel()
	var proofs steerProofs
	first := testSteerDispatch("first-request", "first-input", "Identical prompting.")
	second := testSteerDispatch("second-request", "second-input", "Identical prompting.")
	queued := pendingPrompt("native-pending", first.input.prompt)
	registerSteerStart(t, &proofs, 20, first)
	require.Empty(t, testQueue(t, &proofs, 21, queued).resolutions)
	registerSteerStart(t, &proofs, 22, second)
	assert.Equal(t, []agent.SteerResolution{{InputID: "first-input", AttemptID: "attempt-first-input", Outcome: agent.SteerOutcomeUncertain}},
		testQueue(t, &proofs, 23, queued).resolutions)
	require.Empty(t, testQueue(t, &proofs, 24).resolutions)
	want := second.resolution
	want.Outcome = agent.SteerOutcomeEchoed
	assert.Equal(t, []agent.SteerResolution{want}, feedSteerProof(t, &proofs, 25, eventPendingPromptSubmitted, map[string]any{"prompt": queued}).resolutions)
}

func TestSubmittedSteerDoesNotJoinTheLeadAfterNativeDone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.sessionID()
	request := testSteerDispatch("steer-request", "queued-input", "Later steer.")
	request.resolution.Receipt.SessionID = sessionID
	r.agent.Mu.Lock()
	r.agent.turn = turnState{active: true, startedAt: r.agent.clock.Now(), inputReply: &inputReply{
		started: true, receipt: agent.NativeInputReceipt{SessionID: sessionID, InputID: "root", TurnID: "root"},
	}}
	r.agent.steerProofs.register(request)
	r.agent.Mu.Unlock()
	feed := func(sequence int64, name string, payload any) {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		r.agent.handleEvent(hubEvent{Event: name, SessionID: sessionID, Sequence: sequence, Payload: raw})
	}
	queued := pendingPrompt("native-pending", request.input.prompt)
	feed(9, eventRunStarted, map[string]string{"requestId": "steer-request", "clientId": r.agent.clientID})
	feed(10, eventPendingPrompts, map[string]any{"prompts": []nativePendingPrompt{queued}})
	feed(11, eventAgentDone, map[string]string{"reason": "completed"})
	feed(12, eventPendingPrompts, map[string]any{"prompts": []nativePendingPrompt{}})
	feed(13, eventPendingPromptSubmitted, map[string]any{"prompt": queued})
	want := request.resolution
	want.Outcome = agent.SteerOutcomeEchoed
	assert.Equal(t, []agent.SteerResolution{want}, r.sink.SteerResolutions())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		request.resolution.Receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, r.sink.NativeReplyRetirements())
}

func TestSteerProofsIgnoreUnsequencedEvents(t *testing.T) {
	t.Parallel()
	var proofs steerProofs
	request := testSteerDispatch("request", "input", "The revised prompt.")
	queued := pendingPrompt("native-pending", "The revised prompt.")
	registerSteerStart(t, &proofs, 7, request)
	// An unsequenced heartbeat (Sequence == 0) between durable sequence 7 and 8 must not break the pair.
	hb := feedSteerProof(t, &proofs, 0, eventRunHeartbeat, nil)
	assert.False(t, hb.sequenceGap)
	assert.Empty(t, hb.resolutions)
	got := testQueue(t, &proofs, 8, queued)
	assert.Empty(t, got.resolutions)
	assert.True(t, request.admitted)
}

func TestSteerProofsUnsequencedAndNegativeSequenceEvents(t *testing.T) {
	t.Parallel()
	t.Run("unsequenced run.started resolves own steer uncertain", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		req := testSteerDispatch("req-0", "in-0", "Prompt.")
		proofs.register(req)
		obs := feedSteerProof(t, &proofs, 0, eventRunStarted, map[string]string{
			"requestId": "req-0", "clientId": steerTestClient,
		})
		assert.False(t, obs.ownStart)
		assert.Equal(t, []agent.SteerResolution{{InputID: "in-0", AttemptID: "attempt-in-0", Outcome: agent.SteerOutcomeUncertain}}, obs.resolutions)
	})

	t.Run("unsequenced pending_prompts breaks admission pair", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		req := testSteerDispatch("req-1", "in-1", "Prompt.")
		registerSteerStart(t, &proofs, 7, req)
		obs := testQueue(t, &proofs, 0, pendingPrompt("p1", "Prompt."))
		assert.True(t, obs.uncertainRun)
		assert.Equal(t, []agent.SteerResolution{{InputID: "in-1", AttemptID: "attempt-in-1", Outcome: agent.SteerOutcomeUncertain}}, obs.resolutions)
	})

	t.Run("unsequenced pending_prompt_submitted breaks submission pair", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		req := testSteerDispatch("req-2", "in-2", "Prompt.")
		queued := pendingPrompt("p2", "Prompt.")
		registerSteerStart(t, &proofs, 7, req)
		require.Empty(t, testQueue(t, &proofs, 8, queued).resolutions)
		require.Empty(t, testQueue(t, &proofs, 9).resolutions)
		obs := feedSteerProof(t, &proofs, 0, eventPendingPromptSubmitted, map[string]any{"prompt": queued})
		assert.Equal(t, []agent.SteerResolution{{InputID: "in-2", AttemptID: "attempt-in-2", Outcome: agent.SteerOutcomeUncertain}}, obs.resolutions)
	})

	t.Run("negative sequence breaks admission and marks queue invalid", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		req := testSteerDispatch("req-neg", "in-neg", "Prompt.")
		registerSteerStart(t, &proofs, 7, req)
		obs := testQueue(t, &proofs, -1, pendingPrompt("p-neg", "Prompt."))
		assert.True(t, obs.uncertainRun)
		assert.Equal(t, []agent.SteerResolution{{InputID: "in-neg", AttemptID: "attempt-in-neg", Outcome: agent.SteerOutcomeUncertain}}, obs.resolutions)
		assert.Nil(t, proofs.queue)
	})
}

func TestDecodeSteerEventMalformedAndNullPayloads(t *testing.T) {
	t.Parallel()
	for _, eventName := range []string{eventRunStarted, eventPendingPrompts, eventPendingPromptSubmitted} {
		for _, payload := range []string{``, `null`, `"bad"`, `[]`, `{broken`} {
			decoded := decodeSteerEvent(hubEvent{Event: eventName, Payload: json.RawMessage(payload)})
			assert.Nil(t, decoded.runStarted, "%s %s", eventName, payload)
			if payload != `null` || eventName == eventRunStarted {
				// For pending_prompts / pending_prompt_submitted, JSON `null` unmarshals into a zero struct value cleanly.
				assert.Nil(t, decoded.prompts, "%s %s", eventName, payload)
				assert.Nil(t, decoded.submitted, "%s %s", eventName, payload)
			}
		}
	}
}

func TestSteerProofsObserveQueueRejectsEmptyAndDuplicatePromptIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prompts []nativePendingPrompt
	}{
		{name: "empty prompt ID", prompts: []nativePendingPrompt{pendingPrompt("", "Prompt.")}},
		{name: "duplicate prompt ID", prompts: []nativePendingPrompt{pendingPrompt("dup", "Prompt."), pendingPrompt("dup", "Prompt.")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var proofs steerProofs
			req := testSteerDispatch("req-id", "in-id", "Prompt.")
			registerSteerStart(t, &proofs, 7, req)
			obs := testQueue(t, &proofs, 8, tc.prompts...)
			assert.True(t, obs.uncertainRun)
			assert.Equal(t, []agent.SteerResolution{{InputID: "in-id", AttemptID: "attempt-in-id", Outcome: agent.SteerOutcomeUncertain}}, obs.resolutions)
			assert.Nil(t, proofs.queue)
		})
	}
}

func TestSteerProofsDeterministicSortedResolutions(t *testing.T) {
	t.Parallel()
	t.Run("observeQueue sorts pending resolutions by prompt ID", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		proofs.pending = map[string]*steerDispatch{
			"p-z": testSteerDispatch("r-z", "in-z", "z"),
			"p-a": testSteerDispatch("r-a", "in-a", "a"),
			"p-m": testSteerDispatch("r-m", "in-m", "m"),
		}
		proofs.queue = map[string]struct{}{"p-z": {}, "p-a": {}, "p-m": {}}
		proofs.lastSequence = 10
		// Invalid payload (empty ID) forces all pending prompts to resolve uncertain in sorted prompt ID order.
		obs := testQueue(t, &proofs, 11, pendingPrompt("", "bad"))
		require.Len(t, obs.resolutions, 3)
		assert.Equal(t, []string{"in-a", "in-m", "in-z"}, []string{
			obs.resolutions[0].InputID, obs.resolutions[1].InputID, obs.resolutions[2].InputID,
		})
	})

	t.Run("reset sorts writes by request ID then pending by prompt ID", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		proofs.register(testSteerDispatch("req-z", "w-z", "wz"))
		proofs.register(testSteerDispatch("req-a", "w-a", "wa"))
		proofs.pending = map[string]*steerDispatch{
			"p-z": testSteerDispatch("pr-z", "p-z", "pz"),
			"p-a": testSteerDispatch("pr-a", "p-a", "pa"),
		}
		resolutions := proofs.reset()
		require.Len(t, resolutions, 4)
		assert.Equal(t, []string{"w-a", "w-z", "p-a", "p-z"}, []string{
			resolutions[0].InputID, resolutions[1].InputID, resolutions[2].InputID, resolutions[3].InputID,
		})
	})

	t.Run("discardObserved sorts pending by prompt ID before admission and submission", func(t *testing.T) {
		t.Parallel()
		var proofs steerProofs
		proofs.pending = map[string]*steerDispatch{
			"p-c": testSteerDispatch("rc", "in-c", "c"),
			"p-a": testSteerDispatch("ra", "in-a", "a"),
			"p-b": testSteerDispatch("rb", "in-b", "b"),
		}
		proofs.admission = &steerEventPair{sequence: 5, dispatch: testSteerDispatch("r-adm", "in-adm", "adm")}
		proofs.submission = &steerEventPair{sequence: 6, promptID: "p-sub", dispatch: testSteerDispatch("r-sub", "in-sub", "sub")}
		var obs steerObservation
		proofs.discardObserved(&obs)
		require.Len(t, obs.resolutions, 5)
		assert.Equal(t, []string{"in-a", "in-b", "in-c", "in-adm", "in-sub"}, []string{
			obs.resolutions[0].InputID, obs.resolutions[1].InputID, obs.resolutions[2].InputID,
			obs.resolutions[3].InputID, obs.resolutions[4].InputID,
		})
		assert.True(t, obs.uncertainRun)
	})
}

func TestClineBindSubmittedSteerEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("drained steer when idle starts autonomous steerable turn", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		receipt := agent.NativeInputReceipt{SessionID: r.sessionID(), InputID: "idle-drained-1"}
		r.agent.dispatchMu.Lock()
		r.agent.bindSubmittedSteer(&receipt)
		r.agent.dispatchMu.Unlock()

		assert.Equal(t, "idle-drained-1", receipt.TurnID)
		require.True(t, r.turnActive())
		r.agent.Mu.Lock()
		assert.True(t, r.agent.turn.steerable)
		require.NotNil(t, r.agent.turn.inputReply)
		assert.True(t, r.agent.turn.inputReply.autonomous)
		assert.Equal(t, receipt, r.agent.turn.inputReply.receipt)
		r.agent.Mu.Unlock()
	})

	t.Run("settling turn leaves receipt unbound and retires it unproved", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.Mu.Lock()
		r.agent.turn = turnState{active: true, settling: true, startedAt: r.agent.clock.Now()}
		r.agent.Mu.Unlock()

		receipt := agent.NativeInputReceipt{SessionID: r.sessionID(), InputID: "settling-steer"}
		r.agent.dispatchMu.Lock()
		r.agent.bindSubmittedSteer(&receipt)
		r.agent.dispatchMu.Unlock()
		assert.Empty(t, receipt.TurnID)

		r.agent.retainSteerResolutions([]agent.SteerResolution{{
			InputID: "q-settling", AttemptID: "a-1", Outcome: agent.SteerOutcomeEchoed, Receipt: receipt,
		}})
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			r.sink.NativeReplyRetirements()[receipt])
	})

	t.Run("stopped agent leaves receipt unbound", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.cancel()

		receipt := agent.NativeInputReceipt{SessionID: r.sessionID(), InputID: "stopped-steer"}
		r.agent.dispatchMu.Lock()
		r.agent.bindSubmittedSteer(&receipt)
		r.agent.dispatchMu.Unlock()
		assert.Empty(t, receipt.TurnID)
		assert.False(t, r.turnActive())
	})

	t.Run("active turn with reply.end already recorded marks reply ambiguous", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		reply := &inputReply{
			receipt: agent.NativeInputReceipt{SessionID: sessionID, InputID: "root-1", TurnID: "root-1"},
			started: true,
			end:     &nativeRunEnd{sessionID: sessionID, eventID: "ev-1"},
		}
		r.agent.Mu.Lock()
		r.agent.turn = turnState{active: true, steerable: true, startedAt: r.agent.clock.Now(), inputReply: reply}
		r.agent.Mu.Unlock()

		receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: "late-steer"}
		r.agent.dispatchMu.Lock()
		r.agent.bindSubmittedSteer(&receipt)
		r.agent.dispatchMu.Unlock()
		assert.Empty(t, receipt.TurnID)
		r.agent.Mu.Lock()
		assert.True(t, reply.ambiguous)
		assert.Empty(t, reply.steers)
		r.agent.Mu.Unlock()
	})
}
