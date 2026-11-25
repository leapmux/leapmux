package cline

import (
	"encoding/json"
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
