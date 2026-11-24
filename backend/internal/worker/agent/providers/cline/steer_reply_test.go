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
	return proofs.observe(hubEvent{Event: name, SessionID: steerTestSession, Sequence: sequence, Payload: raw}, steerTestClient)
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
