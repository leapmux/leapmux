package cline

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestInputReplyUsesTheMatchingNativeResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		event         string
		endReason     string
		result        string
		replyFirst    bool
		competingRun  bool
		differentText bool
		outputPastEnd bool
		dropReply     bool
		want          leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "result following the end", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted, result: runReasonCompleted,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		},
		{
			name: "result preceding the end", event: contracts.ClineEventRunAborted, replyFirst: true,
			endReason: runReasonAborted, result: runReasonAborted,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "native failure", event: contracts.ClineEventRunFailed,
			endReason: runReasonError, result: runReasonError,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "mistake limit", event: contracts.ClineEventRunCompleted,
			endReason: contracts.ClineRunReasonMistakeLimit, result: contracts.ClineRunReasonMistakeLimit,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "different results", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted, result: runReasonAborted,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "same reason for different results", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted, result: runReasonCompleted, differentText: true,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "output after the native end", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted, result: runReasonCompleted, outputPastEnd: true,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "queued input acknowledgment", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted,
			want:      leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "lost reply", event: contracts.ClineEventRunCompleted,
			endReason: runReasonCompleted, dropReply: true,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "another run from the same client", event: contracts.ClineEventRunCompleted, competingRun: true,
			endReason: runReasonCompleted, result: runReasonCompleted,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			receipt, err := r.agent.SendInput("Report the status.", nil)
			require.NoError(t, err)
			require.Equal(t, r.sessionID(), receipt.SessionID)
			command, ok := r.hub.waitCommand(commandSessionSendInput)
			require.True(t, ok)
			require.NotEmpty(t, receipt.InputID)
			require.Equal(t, command.RequestID, receipt.InputID)
			if tc.competingRun {
				r.feed(t, eventRunStarted, map[string]any{"requestId": "other-request", "clientId": command.ClientID})
			}
			r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The tests passed."})
			rows := r.sink.Messages()
			require.Len(t, rows, 1)
			if tc.competingRun {
				assert.Empty(t, rows[0].NativeTurnID)
			} else {
				assert.Equal(t, receipt.TurnID, rows[0].NativeTurnID)
			}

			payload := map[string]any{}
			if tc.result != "" {
				text := "The tests passed."
				if tc.differentText {
					text = "The other run completed."
				}
				payload["result"] = map[string]any{"finishReason": tc.result, "text": text}
			}
			if tc.replyFirst {
				r.hub.reply(command.RequestID, fakeReply{Payload: payload})
				waitFor(t, func() bool {
					r.agent.Mu.Lock()
					defer r.agent.Mu.Unlock()
					return r.agent.turn.inputReply != nil && r.agent.turn.inputReply.resultSeen
				}, "the command result is recorded before the end")
			}
			r.feed(t, tc.event, map[string]any{
				"reason": tc.endReason,
				"result": map[string]any{"finishReason": tc.endReason, "text": "The tests passed."},
			})
			rowCount := 2
			if tc.outputPastEnd {
				r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The next run started."})
				tail := r.sink.Messages()
				require.Len(t, tail, 2)
				assert.Empty(t, tail[1].NativeTurnID)
				rowCount++
			}
			switch {
			case tc.replyFirst:
			case tc.dropReply:
				assert.True(t, r.turnActive())
				r.hub.drop()
			default:
				if !tc.competingRun {
					assert.True(t, r.turnActive())
					assert.Len(t, r.sink.Messages(), rowCount-1)
				}
				r.hub.reply(command.RequestID, fakeReply{Payload: payload})
			}
			waitFor(t, func() bool { return !r.turnActive() }, "the native end and command result settle the turn")
			rows = r.sink.Messages()
			require.Len(t, rows, rowCount)
			end := rows[rowCount-1]
			assert.Equal(t, receipt.SessionID, end.AgentSessionID)
			assert.Equal(t, receipt.TurnID, end.NativeTurnID)
			assert.Equal(t, tc.want, end.InputTurnOutcome)
		})
	}
}

func TestClearContextRetiresTheUnfinishedInputReply(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	receipt, err := r.agent.SendInput("Report the status.", nil)
	require.NoError(t, err)
	newSession, err := r.agent.ClearContext()
	require.NoError(t, err)
	assert.NotEqual(t, receipt.SessionID, newSession)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		r.sink.NativeReplyRetirements()[receipt])
}

func TestDrainedSteerSeparatesItsReplyFromThePriorCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		replyText string
		gap       bool
		wantLead  bool
		wantSteer leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "matching prior result", replyText: "Lead reply.", wantLead: true,
			wantSteer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		},
		{
			name: "different prior result", replyText: "Another run's reply.",
			wantSteer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		},
		{
			name: "missing event during the drained run", replyText: "Lead reply.", wantLead: true, gap: true,
			wantSteer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			lead, err := r.agent.SendInput("Initial input.", nil)
			require.NoError(t, err)
			command, ok := r.hub.waitCommand(commandSessionSendInput)
			require.True(t, ok)
			steer := testSteerDispatch("later-request", "later-input", "Later input.")
			steer.resolution.Receipt.SessionID = lead.SessionID
			r.agent.Mu.Lock()
			r.agent.steerProofs.register(steer)
			seq := r.agent.steerProofs.lastSequence
			r.agent.Mu.Unlock()
			feed := func(event string, payload any) {
				t.Helper()
				seq++
				raw, err := json.Marshal(payload)
				require.NoError(t, err)
				r.agent.handleEvent(hubEvent{
					Event: event, SessionID: lead.SessionID, Sequence: seq,
					Payload: raw, Raw: eventEnvelope(t, lead.SessionID, event, payload),
				})
			}
			status := func(status string) {
				feed(eventSessionUpdated, map[string]any{"snapshot": map[string]string{"status": status}})
			}
			queued := pendingPrompt("native-queued", steer.input.prompt)
			feed(eventRunStarted, map[string]string{"requestId": steer.resolution.Receipt.InputID, "clientId": command.ClientID})
			feed(eventPendingPrompts, map[string]any{"prompts": []nativePendingPrompt{queued}})
			feed(contracts.ClineEventAssistantFinished, map[string]string{"text": "Lead reply."})
			feed(eventAgentDone, map[string]string{"reason": runReasonCompleted})
			status(sessionStatusIdle)
			feed(eventPendingPrompts, map[string]any{"prompts": []nativePendingPrompt{}})
			feed(eventPendingPromptSubmitted, map[string]any{"prompt": queued})
			wantBinding := steer.resolution
			wantBinding.Receipt.TurnID = wantBinding.Receipt.InputID
			wantBinding.Outcome = agent.SteerOutcomeEchoed
			require.Equal(t, []agent.SteerResolution{wantBinding}, r.sink.SteerResolutions())
			require.True(t, r.turnActive())
			require.Empty(t, r.sink.NativeReplyTurns())

			status(sessionStatusRunning)
			feed(contracts.ClineEventAssistantFinished, map[string]string{"text": "Reply to the later input."})
			feed(contracts.ClineEventRunCompleted, map[string]any{
				"reason": runReasonCompleted,
				"result": map[string]string{"finishReason": runReasonCompleted, "text": "Lead reply."},
			})
			r.hub.reply(command.RequestID, fakeReply{Payload: map[string]any{
				"result": map[string]string{"finishReason": runReasonCompleted, "text": tc.replyText},
			}})
			waitFor(t, func() bool {
				if tc.wantLead {
					return len(r.sink.NativeReplyTurns()) == 1
				}
				return r.sink.NativeReplyRetirements()[lead] == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
			}, "the prior command settles while the drained run is active")
			require.True(t, r.turnActive())
			rows := r.sink.Messages()
			require.Len(t, rows, 3)
			if tc.wantLead {
				require.NotEmpty(t, rows[0].IdempotencyKey)
				assert.Equal(t, []agent.NativeReplyTurn{{
					SessionID: lead.SessionID, TurnID: lead.TurnID,
					Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: rows[0].IdempotencyKey}},
					Inputs:  []agent.NativeReplyBoundary{{InputID: lead.InputID, AfterOrdinal: 1}},
				}}, r.sink.NativeReplyTurns())
			} else {
				assert.Empty(t, r.sink.NativeReplyTurns())
			}
			assert.Equal(t, lead.TurnID, rows[1].NativeTurnID)
			assert.Equal(t, wantBinding.Receipt.TurnID, rows[2].NativeTurnID)
			if tc.gap {
				seq++
			}
			feed(eventAgentDone, map[string]string{"reason": runReasonCompleted})
			status(sessionStatusIdle)
			require.False(t, r.turnActive())
			rows = r.sink.Messages()
			require.Len(t, rows, 4)
			assert.Equal(t, wantBinding.Receipt.TurnID, rows[3].NativeTurnID)
			assert.Equal(t, tc.wantSteer, rows[3].InputTurnOutcome)
		})
	}
}

func TestClineSteerInputReturnsEmptyReceiptWhenRowAtEcho(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.agent.SendInput("Start lead.", nil)
	require.NoError(t, err)
	_, ok := r.hub.waitCommand(commandSessionSendInput)
	require.True(t, ok)

	type steerRes struct {
		receipt agent.NativeInputReceipt
		err     error
	}
	resCh := make(chan steerRes, 1)
	go func() {
		receipt, steerErr := r.agent.SteerInput(agent.SteerRequest{
			InputID: "steer-q-1", AttemptID: "att-1", Content: "Steered input.",
		})
		resCh <- steerRes{receipt: receipt, err: steerErr}
	}()
	steerCmd, ok := r.hub.waitCommand(commandSessionSendInput)
	require.True(t, ok)
	r.agent.Mu.Lock()
	seq := r.agent.steerProofs.lastSequence
	r.agent.Mu.Unlock()
	feed := func(event string, payload any) {
		seq++
		raw, marshalErr := json.Marshal(payload)
		require.NoError(t, marshalErr)
		r.agent.handleEvent(hubEvent{Event: event, SessionID: r.sessionID(), Sequence: seq, Payload: raw})
	}
	queued := pendingPrompt("native-p1", "Steered input.")
	feed(eventRunStarted, map[string]string{"requestId": steerCmd.RequestID, "clientId": steerCmd.ClientID})
	feed(eventPendingPrompts, map[string]any{"prompts": []nativePendingPrompt{queued}})
	r.hub.reply(steerCmd.RequestID, fakeReply{Payload: map[string]any{"queued": true}})
	res := <-resCh
	require.NoError(t, res.err)
	assert.Empty(t, res.receipt, "SteerRowAtEcho provider must return empty receipt from SteerInput")
}

func TestClineConflictingRunEndEndsTurnInsteadOfStalling(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	receipt, err := r.agent.SendInput("Report status.", nil)
	require.NoError(t, err)
	_, ok := r.hub.waitCommand(commandSessionSendInput)
	require.True(t, ok)

	feedEnd := func(eventID string) {
		payload, marshalErr := json.Marshal(map[string]any{
			"reason": runReasonCompleted,
			"result": map[string]any{"finishReason": runReasonCompleted, "text": "Done."},
		})
		require.NoError(t, marshalErr)
		r.agent.handleEvent(hubEvent{
			EventID: eventID, Event: contracts.ClineEventRunCompleted, SessionID: receipt.SessionID,
			Payload: payload, Raw: eventEnvelope(t, receipt.SessionID, contracts.ClineEventRunCompleted, map[string]any{"reason": runReasonCompleted}),
		})
	}
	feedEnd("ev-1")
	require.True(t, r.turnActive())
	feedEnd("ev-2")
	assert.False(t, r.turnActive(), "conflicting run end must settle the turn instead of stalling")
	rows := r.sink.Messages()
	require.NotEmpty(t, rows)
	assert.True(t, rows[len(rows)-1].TurnEnd)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, rows[len(rows)-1].InputTurnOutcome)
}

func TestClineAutonomousDrainedRunRetiresSteerBoundToIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.sessionID()
	lead := agent.NativeInputReceipt{SessionID: sessionID, InputID: "drained-1", TurnID: "drained-1"}
	drained := &inputReply{receipt: lead, autonomous: true, started: true}
	r.agent.Mu.Lock()
	r.agent.turn = r.agent.drainedTurn(drained, r.agent.clock.Now())
	r.agent.Mu.Unlock()

	second := agent.NativeInputReceipt{SessionID: sessionID, InputID: "steer-in-auto"}
	r.agent.dispatchMu.Lock()
	r.agent.bindSubmittedSteer(&second)
	r.agent.dispatchMu.Unlock()
	require.Equal(t, lead.TurnID, second.TurnID)

	r.agent.retireInputReply(drained)
	retirements := r.sink.NativeReplyRetirements()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[second])
}

func TestClineInputResultMatchesAcrossJSONKeyOrder(t *testing.T) {
	t.Parallel()
	left := readInputResult(json.RawMessage(`{"finishReason":"completed","text":"hello","messages":[{"role":"user"}],"model":{"id":"gpt-5.6","info":{"maxTokens":128000}}}`))
	right := readInputResult(json.RawMessage(`{"text":"hello","finishReason":"completed","toolCalls":[{"id":"t1"}],"model":{"id":"gpt-5.6","info":{"maxTokens":256000}}}`))
	require.Equal(t, "completed", left.reason)
	assert.NotEqual(t, [sha256.Size]byte{}, left.digest, "digest must be populated")
	assert.Equal(t, left, right, "equivalent AgentResult payloads (ignoring messages, toolCalls, model.info) must produce the same canonical digest")

	diffText := readInputResult(json.RawMessage(`{"finishReason":"completed","text":"different","model":{"id":"gpt-5.6"}}`))
	assert.NotEqual(t, left.digest, diffText.digest, "different text must produce different digest")

	diffModelID := readInputResult(json.RawMessage(`{"finishReason":"completed","text":"hello","model":{"id":"gpt-4o"}}`))
	assert.NotEqual(t, left.digest, diffModelID.digest, "model.id must be preserved in the digest")

	for _, invalid := range []string{``, `null`, `[]`, `"text"`, `{}`, `{"finishReason":""}`, `{"finishReason":123}`} {
		assert.Equal(t, inputResult{}, readInputResult(json.RawMessage(invalid)), invalid)
	}
}

func TestClineNativeRunEndOutcomeEdgeCases(t *testing.T) {
	t.Parallel()
	res := func(reason string) inputResult {
		return readInputResult(json.RawMessage(`{"finishReason":"` + reason + `","text":"ok"}`))
	}
	baseReply := func(reason string) *inputReply {
		return &inputReply{
			receipt:    agent.NativeInputReceipt{SessionID: "s1", InputID: "in1", TurnID: "in1"},
			started:    true,
			resultSeen: true,
			result:     res(reason),
		}
	}

	// Valid combinations.
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunCompleted, reason: contracts.ClineRunReasonMaxIterations, result: res(contracts.ClineRunReasonMaxIterations)}.outcome(baseReply(contracts.ClineRunReasonMaxIterations)))
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunFailed, reason: contracts.ClineRunReasonMistakeLimit, result: res(contracts.ClineRunReasonMistakeLimit)}.outcome(baseReply(contracts.ClineRunReasonMistakeLimit)))

	// Mismatched or incomplete cases must return UNSPECIFIED.
	for _, tc := range []struct {
		name  string
		end   nativeRunEnd
		reply *inputReply
	}{
		{
			name:  "nil reply",
			end:   nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunCompleted, reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: nil,
		},
		{
			name:  "wrong session",
			end:   nativeRunEnd{sessionID: "other", eventName: contracts.ClineEventRunCompleted, reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: baseReply(runReasonCompleted),
		},
		{
			name: "result not seen",
			end:  nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunCompleted, reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: &inputReply{
				receipt: agent.NativeInputReceipt{SessionID: "s1", InputID: "in1", TurnID: "in1"},
				started: true,
			},
		},
		{
			name:  "end reason differs from result reason",
			end:   nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunCompleted, reason: runReasonCompleted, result: res(runReasonAborted)},
			reply: baseReply(runReasonAborted),
		},
		{
			name:  "run.completed with aborted reason",
			end:   nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunCompleted, reason: runReasonAborted, result: res(runReasonAborted)},
			reply: baseReply(runReasonAborted),
		},
		{
			name:  "run.aborted with completed reason",
			end:   nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunAborted, reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: baseReply(runReasonCompleted),
		},
		{
			name:  "run.failed with completed reason",
			end:   nativeRunEnd{sessionID: "s1", eventName: contracts.ClineEventRunFailed, reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: baseReply(runReasonCompleted),
		},
		{
			name:  "unknown event name",
			end:   nativeRunEnd{sessionID: "s1", eventName: "run.unknown", reason: runReasonCompleted, result: res(runReasonCompleted)},
			reply: baseReply(runReasonCompleted),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED, tc.end.outcome(tc.reply))
		})
	}
}

func TestClineNonSteerRunStartedRetiresPendingDetachedReply(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	detachedReceipt := agent.NativeInputReceipt{SessionID: r.sessionID(), InputID: "detached-1", TurnID: "detached-1"}
	r.agent.Mu.Lock()
	r.agent.detachedReply = &inputReply{receipt: detachedReceipt, started: true}
	r.agent.Mu.Unlock()

	r.feed(t, eventRunStarted, map[string]any{"requestId": "other-req", "clientId": "other-client"})
	r.agent.Mu.Lock()
	assert.Nil(t, r.agent.detachedReply)
	r.agent.Mu.Unlock()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		r.sink.NativeReplyRetirements()[detachedReceipt])
}

func TestClineAutonomousDrainedRunOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		leadDoneReason string
		wantOutcome    leapmuxv1.AgentInputTurnOutcome
		wantCompletion agent.MessageCompletion
	}{
		{
			name:           "aborted",
			leadDoneReason: contracts.ClineRunReasonAborted,
			wantOutcome:    leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
			wantCompletion: agent.MessageCompletionInterrupted,
		},
		{
			name:           "error",
			leadDoneReason: contracts.ClineRunReasonError,
			wantOutcome:    leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
			wantCompletion: agent.MessageCompletionError,
		},
		{
			name:           "mistake_limit",
			leadDoneReason: contracts.ClineRunReasonMistakeLimit,
			wantOutcome:    leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
			wantCompletion: agent.MessageCompletionError,
		},
		{
			name:           "max_iterations",
			leadDoneReason: contracts.ClineRunReasonMaxIterations,
			wantOutcome:    leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			wantCompletion: agent.MessageCompletionComplete,
		},
		{
			name:           "empty leadDoneReason",
			leadDoneReason: "",
			wantOutcome:    leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			wantCompletion: agent.MessageCompletionComplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			sessionID := r.sessionID()
			receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: "auto-1", TurnID: "auto-1"}
			drained := &inputReply{receipt: receipt, autonomous: true}
			r.agent.Mu.Lock()
			r.agent.turn = r.agent.drainedTurn(drained, r.agent.clock.Now())
			r.agent.Mu.Unlock()

			r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": sessionStatusRunning}})
			if tc.leadDoneReason != "" {
				r.feed(t, eventAgentDone, map[string]any{"reason": tc.leadDoneReason})
			}
			r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": sessionStatusIdle}})

			require.False(t, r.turnActive())
			rows := r.sink.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TurnEnd)
			assert.Equal(t, tc.wantOutcome, rows[0].InputTurnOutcome)
			assert.Equal(t, tc.wantCompletion, rows[0].Completion)
		})
	}

	t.Run("run.failed without result ends autonomous drained run as FAILED", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: "auto-fail", TurnID: "auto-fail"}
		drained := &inputReply{receipt: receipt, autonomous: true, started: true}
		r.agent.Mu.Lock()
		r.agent.turn = r.agent.drainedTurn(drained, r.agent.clock.Now())
		r.agent.Mu.Unlock()

		// A result-bearing run.completed belongs to a prior command and is ignored on an autonomous run.
		r.feed(t, contracts.ClineEventRunCompleted, map[string]any{
			"reason": runReasonCompleted,
			"result": map[string]any{"finishReason": runReasonCompleted, "text": "prior"},
		})
		require.True(t, r.turnActive())

		// A result-free run.failed with reason "error" ends the autonomous drained run as FAILED.
		r.feed(t, contracts.ClineEventRunFailed, map[string]any{"reason": contracts.ClineRunReasonError})
		require.False(t, r.turnActive())
		rows := r.sink.Messages()
		require.Len(t, rows, 1)
		assert.True(t, rows[0].TurnEnd)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, rows[0].InputTurnOutcome)
		assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
	})

	t.Run("run.failed on autonomous drained run with active spawn ends as UNPROVED", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: "auto-spawn", TurnID: "auto-spawn"}
		drained := &inputReply{receipt: receipt, autonomous: true, started: true}
		r.agent.Mu.Lock()
		r.agent.turn = r.agent.drainedTurn(drained, r.agent.clock.Now())
		r.agent.out.lead.spawns = append(r.agent.out.lead.spawns, &spawnCall{})
		r.agent.Mu.Unlock()

		r.feed(t, contracts.ClineEventRunFailed, map[string]any{"reason": contracts.ClineRunReasonError})
		require.False(t, r.turnActive())
		rows := r.sink.Messages()
		require.Len(t, rows, 1)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, rows[0].InputTurnOutcome)
	})
}

type failingClinePersistSink struct {
	agent.ProviderServices
	persistMessageErr error
	persistTurnEndErr error
}

func (s *failingClinePersistSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.persistMessageErr != nil {
		return s.persistMessageErr
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func (s *failingClinePersistSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if s.persistTurnEndErr != nil {
		return s.persistTurnEndErr
	}
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func TestClinePersistTranscriptRowReplyTracking(t *testing.T) {
	t.Parallel()
	t.Run("autonomous drained run skips IdempotencyKey and reply.items", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: "auto-row", TurnID: "auto-row"}
		drained := &inputReply{receipt: receipt, autonomous: true, started: true}
		r.agent.Mu.Lock()
		r.agent.turn = r.agent.drainedTurn(drained, r.agent.clock.Now())
		r.agent.Mu.Unlock()

		r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Autonomous reply."})
		rows := r.sink.Messages()
		require.Len(t, rows, 1)
		assert.Equal(t, receipt.SessionID, rows[0].AgentSessionID)
		assert.Equal(t, receipt.TurnID, rows[0].NativeTurnID)
		assert.Empty(t, rows[0].IdempotencyKey)
		r.agent.Mu.Lock()
		assert.Empty(t, drained.items)
		r.agent.Mu.Unlock()
	})

	t.Run("valid vs malformed assistant.media", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		receipt, err := r.agent.SendInput("Send media.", nil)
		require.NoError(t, err)
		_, ok := r.hub.waitCommand(commandSessionSendInput)
		require.True(t, ok)

		r.feed(t, contracts.ClineEventAssistantMedia, map[string]any{"media": map[string]any{"mediaType": "image/png"}})
		r.feed(t, contracts.ClineEventAssistantMedia, map[string]any{"media": "malformed-not-object"})

		rows := r.sink.Messages()
		require.Len(t, rows, 2)
		assert.NotEmpty(t, rows[0].IdempotencyKey, "valid assistant.media must record an idempotency key")
		assert.Empty(t, rows[1].IdempotencyKey, "malformed assistant.media must not record an idempotency key")
		assert.Equal(t, receipt.TurnID, rows[0].NativeTurnID)
		assert.Equal(t, receipt.TurnID, rows[1].NativeTurnID)
		r.agent.Mu.Lock()
		require.Len(t, r.agent.turn.inputReply.items, 1)
		assert.Equal(t, rows[0].IdempotencyKey, r.agent.turn.inputReply.items[0].MessageKey)
		r.agent.Mu.Unlock()
	})

	t.Run("non-retained PersistMessage error marks reply ambiguous", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.agent.SendInput("Hello.", nil)
		require.NoError(t, err)
		_, ok := r.hub.waitCommand(commandSessionSendInput)
		require.True(t, ok)

		r.agent.sink = &failingClinePersistSink{
			ProviderServices:  r.agent.sink,
			persistMessageErr: errors.New("non-retained storage failure"),
		}
		r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Lost reply."})
		r.agent.Mu.Lock()
		ambiguous := r.agent.turn.inputReply.ambiguous
		r.agent.Mu.Unlock()
		assert.True(t, ambiguous)
	})
}

func TestClineHandleRunEndedDuplicateEventIDReplay(t *testing.T) {
	t.Parallel()
	t.Run("duplicate EventID before resultSeen stays active and settles cleanly", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		receipt, err := r.agent.SendInput("Hello.", nil)
		require.NoError(t, err)
		cmd, ok := r.hub.waitCommand(commandSessionSendInput)
		require.True(t, ok)

		endPayload := map[string]any{
			"reason": runReasonCompleted,
			"result": map[string]any{"finishReason": runReasonCompleted, "text": "Done."},
		}
		rawPayload, err := json.Marshal(endPayload)
		require.NoError(t, err)
		endEvent := hubEvent{
			EventID: "ev-dup-1", Event: contracts.ClineEventRunCompleted, SessionID: receipt.SessionID,
			Payload: rawPayload, Raw: eventEnvelope(t, receipt.SessionID, contracts.ClineEventRunCompleted, endPayload),
		}

		r.agent.handleEvent(endEvent)
		require.True(t, r.turnActive())
		// Replaying the same EventID must not mark ambiguous or end the turn early.
		r.agent.handleEvent(endEvent)
		require.True(t, r.turnActive())
		r.agent.Mu.Lock()
		assert.False(t, r.agent.turn.inputReply.ambiguous)
		r.agent.Mu.Unlock()

		r.hub.reply(cmd.RequestID, fakeReply{Payload: map[string]any{
			"result": map[string]any{"finishReason": runReasonCompleted, "text": "Done."},
		}})
		waitFor(t, func() bool { return !r.turnActive() }, "matching result settles turn after duplicate end replay")
		rows := r.sink.Messages()
		require.Len(t, rows, 1)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[0].InputTurnOutcome)
	})

	t.Run("duplicate EventID on detachedReply publishes once when result arrives", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		detached := &inputReply{
			receipt: agent.NativeInputReceipt{SessionID: sessionID, InputID: "det-dup", TurnID: "det-dup"},
			started: true,
		}
		r.agent.Mu.Lock()
		r.agent.detachedReply = detached
		r.agent.Mu.Unlock()

		endPayload := map[string]any{
			"reason": runReasonCompleted,
			"result": map[string]any{"finishReason": runReasonCompleted, "text": "Detached done."},
		}
		rawPayload, err := json.Marshal(endPayload)
		require.NoError(t, err)
		endEvent := hubEvent{
			EventID: "ev-det-1", Event: contracts.ClineEventRunCompleted, SessionID: sessionID,
			Payload: rawPayload, Raw: eventEnvelope(t, sessionID, contracts.ClineEventRunCompleted, endPayload),
		}

		r.agent.handleEvent(endEvent)
		r.agent.handleEvent(endEvent)
		assert.Empty(t, r.sink.NativeReplyTurns())
		assert.Empty(t, r.sink.NativeReplyRetirements())

		r.agent.recordInputResult(sessionID, "det-dup", readInputResult(json.RawMessage(`{"finishReason":"completed","text":"Detached done."}`)))
		require.Len(t, r.sink.NativeReplyTurns(), 1)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, r.sink.NativeReplyTurns()[0].Outcome)
	})
}

func TestClineJavaScriptWhitespaceForQuestionAnswerAndReplyText(t *testing.T) {
	t.Parallel()
	// U+0085 (NEL) is NOT JavaScript whitespace; U+FEFF (BOM) IS JavaScript whitespace.
	nelRow := eventEnvelope(t, "s1", contracts.ClineEventAssistantFinished, map[string]any{"text": "\u0085"})
	bomRow := eventEnvelope(t, "s1", contracts.ClineEventAssistantFinished, map[string]any{"text": "\uFEFF"})
	got, ok := agent.ReplyText(clineProvider{}, agent.MessageContent{Original: nelRow})
	assert.True(t, ok, "U+0085 is visible in JavaScript trim")
	assert.Equal(t, "\u0085", got)
	_, ok = agent.ReplyText(clineProvider{}, agent.MessageContent{Original: bomRow})
	assert.False(t, ok, "U+FEFF is blank in JavaScript trim")

	nelAns := agent.ControlResponseEnvelope("q1", map[string]any{"behavior": "allow", contracts.ClineQuestionAnswerAnswer: "\u0085"})
	bomAns := agent.ControlResponseEnvelope("q1", map[string]any{"behavior": "allow", contracts.ClineQuestionAnswerAnswer: "\uFEFF"})
	ans, ok := questionAnswer(nelAns)
	assert.True(t, ok)
	assert.Equal(t, "\u0085", ans)
	_, ok = questionAnswer(bomAns)
	assert.False(t, ok)
}
