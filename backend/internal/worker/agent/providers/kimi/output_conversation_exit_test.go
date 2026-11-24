package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kimiExitTranscriptScope struct {
	agent.TranscriptScope
	resets int
}

func (scope *kimiExitTranscriptScope) ResetSpans() {
	scope.TranscriptScope.ResetSpans()
	scope.resets++
}

func TestKimiConversationExitSettlesPartialOutputInItsOriginalTranscript(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name       string
		stop       bool
		discard    bool
		completion agent.MessageCompletion
	}{
		{name: "unexpected exit", completion: agent.MessageCompletionError},
		{name: "intentional stop", stop: true, completion: agent.MessageCompletionInterrupted},
		{name: "discarded restart", stop: true, discard: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0"}`))
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			sinks := map[string]*agenttest.Sink{"main": &rig.sink.Sink, "agent-0": child}
			spans := make(map[string]string)
			scopes := make(map[string]*kimiExitTranscriptScope)
			for _, nativeAgent := range []string{"main", "agent-0"} {
				for _, frame := range []string{
					`{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
					`{"type":"item.started","turn_id":"turn","item":{"type":"tool_call","item_id":"call","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"executing","input":{"command":"run the held command"}}}`,
					`{"type":"item.delta","turn_id":"turn","item_id":"call","field":"output","delta":"partial output"}`,
					`{"type":"item.started","turn_id":"turn","item":{"type":"assistant_message","item_id":"answer","step_index":2,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"Partial answer"}}`,
					`{"type":"item.started","turn_id":"turn","item":{"type":"reasoning","item_id":"reason","step_index":2,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","text":"Partial reasoning"}}`,
				} {
					rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), nativeAgent, frame))
				}
				rows := sinks[nativeAgent].Messages()
				require.Len(t, rows, 1, "only the tool declaration is stored before exit")
				require.NotEmpty(t, rows[0].SpanID)
				spans[nativeAgent] = rows[0].SpanID
				if scenario.discard {
					publication := rig.agent.run(nativeAgent).conversation.turns["turn"]
					scope := &kimiExitTranscriptScope{TranscriptScope: publication.scope}
					publication.scope = scope
					scopes[nativeAgent] = scope
				}
			}
			assert.True(t, rig.agent.PublishTurnActive().Active)
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			if scenario.stop {
				rig.agent.SetStoppedForTest(true)
			}
			if scenario.discard {
				rig.agent.DiscardOutput()
				for _, nativeAgent := range []string{"main", "agent-0"} {
					rig.endPublicTurn(t, nativeAgent, "turn", 0, "interrupted")
				}
			}
			rig.agent.SimulateExitForTest()
			require.NoError(t, rig.agent.Wait())
			awaitKimiTestOutput(t, rig.agent.sink)
			for _, nativeAgent := range []string{"main", "agent-0"} {
				rows := sinks[nativeAgent].Messages()
				if scenario.discard {
					assert.Len(t, rows, 1, "%s retains only the already accepted tool declaration", nativeAgent)
					assert.Equal(t, 1, scopes[nativeAgent].resets, nativeAgent)
					continue
				}
				require.Len(t, rows, 4, nativeAgent)
				seen := make(map[string]int)
				for _, row := range rows {
					assert.False(t, row.TurnEnd, "process exit does not create a native turn record")
					view, err := decodeKimiConversationRecord(row.Content)
					require.NoError(t, err)
					require.NotNil(t, view.item)
					assert.Equal(t, nativeAgent, view.record.AgentID)
					assert.Equal(t, "turn", view.record.TurnID)
					assert.Equal(t, "running", view.item.Status)
					assert.Empty(t, view.item.Outcome)
					if view.record.Role == contracts.KimiConversationRecordRoleRequest {
						assert.Equal(t, agent.MessageCompletionComplete, row.Completion)
						continue
					}
					seen[view.item.ItemID]++
					assert.Equal(t, scenario.completion, row.Completion)
					switch view.item.ItemID {
					case "call":
						assert.True(t, row.Closing)
						assert.Equal(t, spans[nativeAgent], row.SpanID)
						assert.JSONEq(t, `"partial output"`, string(view.item.Output))
					case "answer":
						require.NotNil(t, view.item.Text)
						assert.Equal(t, "Partial answer", *view.item.Text)
					case "reason":
						require.NotNil(t, view.item.Text)
						assert.Equal(t, "Partial reasoning", *view.item.Text)
					}
				}
				assert.Equal(t, map[string]int{"call": 1, "answer": 1, "reason": 1}, seen)
			}
			assert.False(t, rig.agent.PublishTurnActive().Active)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "child-task").Status, "the provider has no new coordinator outcome")
			require.NoError(t, rig.agent.Wait())
			count := 4
			if scenario.discard {
				count = 1
				for _, scope := range scopes {
					assert.Equal(t, 1, scope.resets, "a second wait cannot reset an old scope again")
				}
			}
			assert.Len(t, rig.sink.Messages(), count, "a second wait cannot publish root records twice")
			assert.Len(t, child.Messages(), count, "a second wait cannot publish child records twice")
		})
	}
}

func TestKimiConversationRunningOutputKeepsTheKnownTailAndExactTotal(t *testing.T) {
	t.Parallel()
	for _, nativeEnd := range []bool{false, true} {
		t.Run(fmt.Sprintf("native_end=%t", nativeEnd), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			const tool = `"type":"tool_call","item_id":"held","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"printf test"}`
			rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing","output":"initial prefix "}}`)
			chunks := []string{strings.Repeat("A🙂", 2500), "latest output🙂"}
			for _, chunk := range chunks {
				rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.delta","turn_id":"t0","item_id":"held","field":"output","delta":%q}`, chunk))
			}
			full := "initial prefix " + strings.Join(chunks, "")
			tail, truncated := agent.ClipTailBytes(full, kimiLiveOutputLimit)
			require.True(t, truncated)
			assert.True(t, utf8.ValidString(tail))
			require.LessOrEqual(t, len(tail), kimiLiveOutputLimit)

			state := rig.agent.run(kimiMainAgentID).conversation.document.turns["t0"].items["held"]
			require.NotNil(t, state)
			require.JSONEq(t, jsonQuoteKimiTestString(t, tail), string(state.snapshot().Output))
			scope := kimiConversationProgressID(kimiConversationFrame{SessionID: rig.sessionID(), AgentID: kimiMainAgentID, TurnID: "t0"}, "held", contracts.KimiConversationItemKindToolCall)
			var lastTotal int64
			for _, update := range rig.sink.ProgressUpdates() {
				if update.ScopeID == scope && update.Operation == agent.ProgressOutputTotal && update.Exact {
					lastTotal = update.Value
				}
			}
			assert.EqualValues(t, len(full), lastTotal)

			wantedOutput := tail
			if nativeEnd {
				rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{%s,"status":"ended","outcome":"completed","output":%q}}`, tool, full))
				wantedOutput = full
			} else {
				rig.agent.SetStoppedForTest(true)
				rig.agent.SimulateExitForTest()
				require.NoError(t, rig.agent.Wait())
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			require.Len(t, rows, 2)
			view, err := decodeKimiConversationRecord(rows[1].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
			assert.JSONEq(t, jsonQuoteKimiTestString(t, wantedOutput), string(view.item.Output))
			var stored map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rows[1].Content, &stored))
			if nativeEnd {
				assert.NotContains(t, stored, "output_truncated")
			} else {
				assert.Equal(t, json.RawMessage("true"), stored["output_truncated"])
				assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
			}
		})
	}
}

func jsonQuoteKimiTestString(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestKimiNormalTurnKeepsToolOpenAcrossTurnTwoAndMirrorsRootSink(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	const tool = `"type":"tool_call","item_id":"call-1","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)

	rows := rig.sink.Messages()
	require.Len(t, rows, 2, "normal turn end must keep still-running tool open")
	assert.False(t, rows[0].Closing)
	assert.True(t, rows[1].TurnEnd)
	spanID := rows[0].SpanID
	require.NotEmpty(t, spanID)
	assert.Empty(t, rig.sink.ClosedSpans())
	// Root sink mirrors the live open span so OutputHandler sees open root spans.
	assert.Equal(t, "Bash", rig.sink.GetSpanType(spanID))
	assert.NotEmpty(t, rig.sink.OpenSpans())

	// Starting Turn 2 seeds Turn 2's TranscriptScope with Turn 1's still-open span.
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:05Z","steps":[]}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t1", 1, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)

	rows = rig.sink.Messages()
	require.Len(t, rows, 3)
	t1Pub := rig.agent.run(kimiMainAgentID).conversation.turns["t1"]
	require.NotNil(t, t1Pub)
	assert.Equal(t, "Bash", t1Pub.scope.GetSpanType(spanID), "Turn 2 scope must inherit Turn 1 open tool span")
}

func TestKimiInterruptedTurnTwoIdleInterruptAndExitCloseEarlierTurnTools(t *testing.T) {
	t.Parallel()

	t.Run("interrupted turn two closes turn one tool and drops late item.ended", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `"type":"tool_call","item_id":"call-1","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")

		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:05Z","steps":[]}}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t1", 1, "interrupted")
		awaitKimiTestOutput(t, rig.agent.sink)

		spanID := rig.sink.Messages()[0].SpanID
		assert.Contains(t, rig.sink.ClosedSpans(), spanID)
		assert.Contains(t, rig.sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, rig.sink.ProgressUpdates(), agent.ResetProgress())

		countBeforeLate := rig.sink.MessageCount()
		closedBeforeLate := len(rig.sink.ClosedSpans())
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{`+tool+`,"status":"ended","outcome":"completed","output":"late"}}`)
		awaitKimiTestOutput(t, rig.agent.sink)
		assert.Equal(t, countBeforeLate, rig.sink.MessageCount(), "late item.ended must not persist a duplicate closing row")
		assert.Equal(t, closedBeforeLate, len(rig.sink.ClosedSpans()), "late item.ended must not close span twice")
	})

	t.Run("idle interrupt closes earlier turn tool and preserves detached bgtask", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"bg-1","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0"}`))
		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `"type":"tool_call","item_id":"call-idle","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
		awaitKimiTestOutput(t, rig.agent.sink)
		require.Empty(t, rig.sink.ClosedSpans())

		require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))
		awaitKimiTestOutput(t, rig.agent.sink)
		spanID := rig.sink.Messages()[0].SpanID
		assert.Contains(t, rig.sink.ClosedSpans(), spanID)
		assert.Contains(t, rig.sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, rig.sink.ProgressUpdates(), agent.ResetProgress())
		assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "bg-1").Status, "idle root interrupt must preserve detached bgtask")
	})

	t.Run("process exit closes open tool from an earlier normally-ended turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `"type":"tool_call","item_id":"call-exit","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
		awaitKimiTestOutput(t, rig.agent.sink)
		require.Empty(t, rig.sink.ClosedSpans())

		rig.agent.SimulateExitForTest()
		require.NoError(t, rig.agent.Wait())
		awaitKimiTestOutput(t, rig.agent.sink)

		spanID := rig.sink.Messages()[0].SpanID
		assert.Contains(t, rig.sink.ClosedSpans(), spanID)
		assert.Contains(t, rig.sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, rig.sink.ProgressUpdates(), agent.ResetProgress())
		var foundClosing bool
		for _, row := range rig.sink.Messages() {
			if row.Closing && row.SpanID == spanID {
				foundClosing = true
				assert.Equal(t, agent.MessageCompletionError, row.Completion)
			}
		}
		assert.True(t, foundClosing)
	})
}

func TestKimiDiscardingExitResetsSpansAndProgress(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	const tool = `"type":"tool_call","item_id":"call-disc","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Messages(), 1)

	rig.agent.DiscardOutput()
	rig.agent.SimulateExitForTest()
	require.NoError(t, rig.agent.Wait())

	assert.Len(t, rig.sink.Messages(), 1, "discarding exit must not write closing rows")
	assert.Contains(t, rig.sink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, rig.sink.ProgressUpdates(), agent.ResetProgress())
}

func TestKimiCrossTurnOpenToolReportsPostTurnOutputAndCompletesOnNativeEnd(t *testing.T) {
	t.Parallel()

	t.Run("post-turn delta, update, and native item.ended update and complete output progress", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `"type":"tool_call","item_id":"call-1","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 5"}`
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
		rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"call-1","field":"output","delta":"before "}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
		awaitKimiTestOutput(t, rig.agent.sink)

		scopeID := kimiConversationProgressID(kimiConversationFrame{SessionID: rig.sessionID(), AgentID: kimiMainAgentID, TurnID: "t0"}, "call-1", contracts.KimiConversationItemKindToolCall)
		require.EqualValues(t, len("before "), rig.sink.ProgressSnapshot().OutputBytes)

		// Post-turn-end output delta updates live output tail and exact byte count.
		rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"call-1","field":"output","delta":"after"}`)
		assert.EqualValues(t, len("before after"), rig.sink.ProgressSnapshot().OutputBytes)
		total, tail := lastKimiToolOutputProgress(rig)
		assert.Equal(t, scopeID, total.ScopeID)
		assert.EqualValues(t, len("before after"), total.Value)
		assert.Equal(t, scopeID, tail.ScopeID)
		assert.Equal(t, "before after", tail.Text)

		// Post-turn-end item.updated output replacement also updates live output progress.
		rig.conversationFrame(t, `{"type":"item.updated","turn_id":"t0","item_id":"call-1","set":{"output":"replaced output"}}`)
		assert.EqualValues(t, len("replaced output"), rig.sink.ProgressSnapshot().OutputBytes)
		total, tail = lastKimiToolOutputProgress(rig)
		assert.EqualValues(t, len("replaced output"), total.Value)
		assert.Equal(t, "replaced output", tail.Text)

		// Native item.ended completes output progress so OutputBytes drops to zero,
		// while turn-scoped model progress stays refused after turn end.
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{`+tool+`,"status":"ended","outcome":"completed","output":"final output"}}`)
		awaitKimiTestOutput(t, rig.agent.sink)

		assert.Contains(t, rig.sink.ClosedSpans(), scopeID)
		assert.Contains(t, rig.sink.ProgressUpdates(), agent.CompleteOutputProgress(scopeID))
		assert.NotContains(t, rig.sink.ProgressUpdates(), agent.CompleteModelProgress(scopeID), "turn-scoped model progress must refuse after turn end")
		assert.Zero(t, rig.sink.ProgressSnapshot().OutputBytes, "native item.ended must clear OutputBytes")
	})

	t.Run("late item.ended after force-close does not revive output progress", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `"type":"tool_call","item_id":"call-2","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 5"}`
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
		rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"call-2","field":"output","delta":"running"}`)
		rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
		awaitKimiTestOutput(t, rig.agent.sink)

		require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))
		awaitKimiTestOutput(t, rig.agent.sink)
		require.Zero(t, rig.sink.ProgressSnapshot().OutputBytes)

		updatesBeforeLate := len(rig.sink.ProgressUpdates())
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{`+tool+`,"status":"ended","outcome":"completed","output":"late output"}}`)
		awaitKimiTestOutput(t, rig.agent.sink)

		assert.Len(t, rig.sink.ProgressUpdates(), updatesBeforeLate, "late item.ended after force-close must not emit progress updates")
		assert.Zero(t, rig.sink.ProgressSnapshot().OutputBytes)
	})
}

func TestKimiInterruptedTurnTwoClosesEarlierToolBeforeTurnTwoDivider(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const tool = `"type":"tool_call","item_id":"call-1","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")

	rig.startPublicTurn(t, kimiMainAgentID, "t1", 1)
	rig.endPublicTurn(t, kimiMainAgentID, "t1", 1, "interrupted")
	awaitKimiTestOutput(t, rig.agent.sink)

	rows := rig.sink.Messages()
	require.Len(t, rows, 4)
	spanID := rows[0].SpanID
	require.NotEmpty(t, spanID)

	closingIndex := -1
	turnTwoDividerIndex := -1
	for i, row := range rows {
		if row.Closing && row.SpanID == spanID {
			closingIndex = i
		}
		if row.TurnEnd {
			view, err := decodeKimiConversationRecord(row.Content)
			require.NoError(t, err)
			if view.turn != nil && view.turn.TurnID == "t1" {
				turnTwoDividerIndex = i
			}
		}
	}
	require.NotEqual(t, -1, closingIndex, "Turn 1 interrupted closing row must be persisted")
	require.NotEqual(t, -1, turnTwoDividerIndex, "Turn 2 divider must be persisted")
	assert.Less(t, closingIndex, turnTwoDividerIndex, "Turn 1 interrupted closing row must precede Turn 2 TurnEnd divider")
}

func TestKimiTurnsRecoverySnapshotCompletesOutputProgressOnToolClose(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const tool = `"type":"tool_call","item_id":"call-rec","step_index":1,"name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"printf hi"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"call-rec","field":"output","delta":"hello world"}`)
	awaitKimiTestOutput(t, rig.agent.sink)

	scopeID := kimiConversationProgressID(kimiConversationFrame{SessionID: rig.sessionID(), AgentID: kimiMainAgentID, TurnID: "t0"}, "call-rec", contracts.KimiConversationItemKindToolCall)
	require.EqualValues(t, len("hello world"), rig.sink.ProgressSnapshot().OutputBytes)

	// Recovery "turns" snapshot closes the previously open tool_call without an incremental item.ended.
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{`+tool+`,"status":"ended","outcome":"completed","output":"hello world"}]}]}`)
	awaitKimiTestOutput(t, rig.agent.sink)

	assert.Contains(t, rig.sink.ClosedSpans(), scopeID)
	assert.Contains(t, rig.sink.ProgressUpdates(), agent.CompleteOutputProgress(scopeID))
	assert.Zero(t, rig.sink.ProgressSnapshot().OutputBytes, "recovery snapshot tool closure must clear OutputBytes")
}

type kimiFailingCaptureScope struct {
	agent.TranscriptScope
	failAfter int
	calls     int
	resets    int
}

func (s *kimiFailingCaptureScope) CaptureTranscript(content agent.MessageContent, span agent.SpanInfo) (agent.CapturedTranscript, error) {
	s.calls++
	if s.calls > s.failAfter {
		return agent.CapturedTranscript{}, errors.New("injected capture failure")
	}
	return s.TranscriptScope.CaptureTranscript(content, span)
}

func (s *kimiFailingCaptureScope) ResetSpans() {
	s.resets++
	s.TranscriptScope.ResetSpans()
}

func TestKimiFailedCaptureOrStaleSessionDoesNotMutateLiveSpans(t *testing.T) {
	t.Parallel()

	t.Run("failed capture on second row does not leak phantom open live span", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		conv := rig.agent.run(kimiMainAgentID).conversation
		pub := conv.turns["t0"]
		require.NotNil(t, pub)
		// Allow the first row (tool request) to capture, fail on the second row (tool result).
		pub.scope = &kimiFailingCaptureScope{TranscriptScope: pub.scope, failAfter: 1}

		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"call-fail","step_index":1,"name":"Bash","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"command":"echo hi"},"output":"hi"}}`)
		awaitKimiTestOutput(t, rig.agent.sink)

		assert.Empty(t, conv.openLiveSpans, "failed capture must not mutate run.conversation.openLiveSpans")
		assert.Empty(t, rig.sink.OpenSpans(), "failed capture must not open phantom live spans on the sink")
		assert.Empty(t, rig.sink.ClosedSpans(), "failed capture must not close spans on the sink")
	})

	t.Run("stale session publication does not mutate spans on replacement session sink", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		run := rig.agent.run(kimiMainAgentID)
		turn := run.conversation.document.turns["t0"]
		pub := run.conversation.turns["t0"]
		require.NotNil(t, turn)
		require.NotNil(t, pub)

		// Replace the agent's live sink with a fresh session sink while holding the old turn publication.
		replacementSink := &agenttest.ControlSink{}
		replacementServices := agent.NewProviderServices(replacementSink)
		replacementServices.UpdateSessionID("session_replacement")
		rig.agent.sink = replacementServices
		// Also expire the old sink's session so Owner().IsSessionCurrent() is false.
		rig.sink.AdoptNativeSession("session_replacement")

		// Apply a tool item to the old turn and persist through the stale publication.
		frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, rig.sessionID(), kimiMainAgentID, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-stale","step_index":1,"name":"Bash","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"command":"echo stale"}}}`))
		require.NoError(t, err)
		_, err = run.conversation.document.apply(frame)
		require.NoError(t, err)
		_ = rig.agent.persistConversationItems(run, turn, pub, frame, kimiConversationSelection{items: []string{"call-stale"}})

		assert.Empty(t, replacementSink.OpenSpans(), "stale turn publication must not open spans on raw a.sink")
		assert.Empty(t, rig.sink.OpenSpans(), "expired session publication must not mutate live spans on the old sink either")
	})
}

func TestKimiCloseOpenToolsAcrossTurnsPreservesStateOnPersistFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const tool = `"type":"tool_call","item_id":"call-retry","name":"Bash","started_at":"2026-11-18T00:00:00Z","created_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 30"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)

	run := rig.agent.run(kimiMainAgentID)
	pub := run.conversation.turns["t0"]
	require.NotNil(t, pub)
	spanID := rig.sink.Messages()[0].SpanID
	require.NotEmpty(t, spanID)

	realScope := pub.scope
	failingScope := &kimiFailingCaptureScope{TranscriptScope: realScope, failAfter: 0}
	pub.scope = failingScope

	// First attempt fails to persist closing row.
	rig.agent.closeOpenConversationToolsAcrossTurns(run, agent.MessageCompletionInterrupted, true)

	assert.NotContains(t, pub.results, "call-retry", "failed persist must not mark tool as closed in publication.results")
	assert.Zero(t, failingScope.resets, "failed persist must not reset publication.scope")
	assert.NotEmpty(t, run.conversation.openLiveSpans, "failed persist must not clear openLiveSpans")

	// Restore working scope: a later retry or exit must now persist the closing row.
	pub.scope = realScope
	rig.agent.closeOpenConversationToolsAcrossTurns(run, agent.MessageCompletionInterrupted, true)
	awaitKimiTestOutput(t, rig.agent.sink)

	assert.Contains(t, pub.results, "call-retry")
	assert.Empty(t, run.conversation.openLiveSpans)
	var foundClosing bool
	for _, row := range rig.sink.Messages() {
		if row.Closing && row.SpanID == spanID {
			foundClosing = true
			assert.Equal(t, agent.MessageCompletionInterrupted, row.Completion)
		}
	}
	assert.True(t, foundClosing, "retry after transient failure must persist the closing row")
}

type kimiSupersededLeaseOwner struct {
	sessionCurrent bool
	frameCurrent   bool
}

func (o kimiSupersededLeaseOwner) IsCurrent() bool                          { return false }
func (o kimiSupersededLeaseOwner) IsSessionCurrent() bool                   { return o.sessionCurrent }
func (o kimiSupersededLeaseOwner) IsTranscriptFrameCurrent() bool           { return o.frameCurrent }
func (o kimiSupersededLeaseOwner) TranscriptFrameSpans() agent.SpanServices { return nil }
func (o kimiSupersededLeaseOwner) SnapshotTranscriptFrame(agent.SpanInfo) agent.TranscriptPublication {
	return nil
}
func (o kimiSupersededLeaseOwner) PublishSessionInfo(map[string]interface{}) bool { return false }

type kimiStubPublication struct {
	owner agent.TranscriptOwner
}

func (p kimiStubPublication) Owner() agent.TranscriptOwner        { return p.owner }
func (p kimiStubPublication) ReportProgress(agent.ProgressUpdate) {}

func TestKimiCompletedTurnProgressRespectsSupersededChildLease(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	services := agent.NewProviderServices(sink)
	pub := &kimiConversationTurnOutput{
		writer:    &kimiConversationRecordWriter{ProviderServices: services},
		progress:  kimiStubPublication{owner: kimiSupersededLeaseOwner{sessionCurrent: true, frameCurrent: false}},
		completed: true,
	}

	pub.reportProgressUpdate(agent.OutputExactTotalProgress("tool-1", 42))
	assert.Empty(t, sink.ProgressUpdates(), "completed turn from a superseded child lease must not write progress to the shared sink")

	// Also verify with a real agenttest child lease superseded on the same session.
	root := &agenttest.ControlSink{}
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: "child-lease-key",
		Title:            "Child",
	})
	require.NoError(t, err)
	lease1 := root.AcquireChildSink(childID)
	lease1.Activate()
	lease1.Sink.UpdateSessionID("sess-1")
	lease1.Sink.SetTurnState(agent.TurnState{Active: true}, 1)
	captured1 := lease1.CaptureMessage(agent.MessageContent{AgentSessionID: "sess-1"}, agent.SpanInfo{})
	lease1.Sink.SetTurnState(agent.TurnState{Active: false}, 2)

	lease2 := root.AcquireChildSink(childID)
	_ = lease2

	childSink := root.Child(childID)
	before := len(childSink.ProgressUpdates())
	leasedPub := &kimiConversationTurnOutput{
		writer:    &kimiConversationRecordWriter{ProviderServices: lease1.Sink},
		progress:  captured1.Publication,
		completed: true,
	}
	leasedPub.reportProgressUpdate(agent.OutputExactTotalProgress("tool-1", 99))
	assert.Len(t, childSink.ProgressUpdates(), before, "superseded child lease publication must refuse post-turn output progress")
}
