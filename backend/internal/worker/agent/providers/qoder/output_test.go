package qoder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestQoderNonJSONStdoutLineStoresNothingAndKeepsReading(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	require.NoError(t, cmd.Start())
	a, sink, _ := newGoalAgent(t)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "qoder-stdout", ProviderName: "qoder", Ctx: t.Context(), Cmd: cmd})
	scanner := bufio.NewScanner(strings.NewReader("Skill command '/docs' was renamed to '/project-docs'.\n" + qoderSuccessResult + "\n"))
	a.readOutputLoop(scanner)
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
	assert.JSONEq(t, qoderSuccessResult, string(sink.Messages()[0].Content))
}

func TestQoderControlTimesOutOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	processDone := make(chan struct{})
	a := newOfflineAgentWithRuntime(t, &agenttest.Sink{}, clock, processDone)
	t.Cleanup(func() { close(processDone) })
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, "qoder control get_state")
	defer trap.Close()
	result := make(chan error, 1)
	go func() { _, err := a.sendControlAndWait(`{"subtype":"get_state"}`, time.Hour); result <- err }()
	ctx := testutil.DeadlineContext(t)
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, time.Hour, wait)
	clock.Advance(wait).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, errControlTimeout)
	case <-ctx.Done():
		t.Fatal("the control wait did not end at the injected deadline")
	}
}

type readyQoderControlClock struct {
	quartz.Clock
	timer   *quartz.Timer
	onTimer func()
}

func (c readyQoderControlClock) NewTimer(time.Duration, ...string) *quartz.Timer {
	c.onTimer()
	return c.timer
}

func TestQoderControlReplyAtTheDeadlineWins(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	timer := clock.NewTimer(time.Second)
	clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
	stdin := &agenttest.Stdin{}
	var a *Agent
	ready := readyQoderControlClock{Clock: clock, timer: timer, onTimer: func() {
		var request struct {
			RequestID string `json:"request_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdin.String()), &request))
		a.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"mode":"plan"}}}`, request.RequestID)))
	}}
	a = newOfflineAgentWithRuntime(t, &agenttest.Sink{}, ready, nil)
	a.SetStdinForTest(stdin)
	result, err := a.sendControlAndWait(`{"subtype":"set_permission_mode"}`, time.Nanosecond)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "plan", result.Mode)
}

func TestQoderControlFailedWriteUsesTheInjectedClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	processDone := make(chan struct{})
	a := newOfflineAgentWithRuntime(t, &agenttest.Sink{}, clock, processDone)
	t.Cleanup(func() { close(processDone) })
	a.SetStdinForTest(agenttest.FailingStdin{})
	trap := clock.Trap().NewTimer("qoder-send-failure-exit")
	defer trap.Close()
	result := make(chan error, 1)
	go func() { _, err := a.sendControlAndWait(`{"subtype":"get_state"}`, time.Hour); result <- err }()
	ctx := testutil.DeadlineContext(t)
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, time.Second, wait)
	clock.Advance(wait).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("the failed write did not end at the injected deadline")
	}
}

func qoderCompactionValues(sink *agenttest.Sink) []any {
	var values []any
	for _, info := range sink.SessionInfos() {
		if value, ok := info[contracts.SessionInfoKeyCompaction]; ok {
			values = append(values, value)
		}
	}
	return values
}

func TestQoderManualAndAutomaticCompactionAreLiveThenOneBoundary(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{"manual", "auto"} {
		t.Run(trigger, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			// Sources: qoder/run3 frames 58-66 and qoder/run2 frames 86-92.
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting","session_id":"session-1"}`))
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null,"session_id":"session-1"}`))
			assert.Equal(t, []any{map[string]any{}}, qoderCompactionValues(sink))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			boundary := []byte(fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","session_id":"session-1","compact_metadata":{"trigger":%q,"pre_tokens":60000,"post_tokens":10000}}`, trigger))
			a.HandleOutput(boundary)
			assert.Equal(t, []any{map[string]any{}, nil}, qoderCompactionValues(sink))
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.Equal(t, boundary, sink.PersistedNotifications()[0].Content)
			a.HandleOutput([]byte(qoderSuccessResult))
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestQoderFailedOrInterruptedCompactionEnds(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			a.setTurnActive(true)
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting","session_id":"session-1"}`))
			if interrupted {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
			}
			a.HandleOutput([]byte(qoderAbortedResult))
			assert.Equal(t, []any{map[string]any{}, nil}, qoderCompactionValues(sink))
			notices := sink.LeapMuxNotifications()
			require.Len(t, notices, 1)
			want := "failed"
			if interrupted {
				want = "aborted"
			}
			assert.Equal(t, map[string]any{"type": "compaction_ended", "outcome": want}, notices[0])
			require.Len(t, sink.Messages(), 1)
			assert.True(t, sink.Messages()[0].TurnEnd)
		})
	}
}

func TestQoderChildCompactionUsesTheChildSink(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.HandleOutput([]byte(qoderChildStarted))
	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting","parent_tool_use_id":"spawn-1","session_id":"child-session"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"compact_boundary","parent_tool_use_id":"spawn-1","session_id":"child-session"}`))
	assert.Equal(t, []any{map[string]any{}, nil}, qoderCompactionValues(child))
	assert.Empty(t, qoderCompactionValues(sink))
	require.Len(t, child.PersistedNotifications(), 1)
	assert.Equal(t, "child-session", child.PersistedNotifications()[0].AgentSessionID)
}

func TestQoderCompactionBoundaryWithoutAStartClearsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.HandleOutput([]byte(`{"type":"system","subtype":"compact_boundary","session_id":"session-1"}`))
	assert.Equal(t, []any{nil}, qoderCompactionValues(sink))
	assert.Len(t, sink.PersistedNotifications(), 1)
}

func TestQoderChildCompactionStopsWithoutChangingTheRoot(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.HandleOutput([]byte(qoderChildStarted))
	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting","parent_tool_use_id":"spawn-1"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"spawn-1","task_id":"task-1","status":"stopped"}`))
	assert.Equal(t, []any{map[string]any{}, nil}, qoderCompactionValues(child))
	assert.Equal(t, []map[string]any{{"type": "compaction_ended", "outcome": "aborted"}}, child.LeapMuxNotifications())
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestQoderRetryResolvesAtTheFirstOutput(t *testing.T) {
	t.Parallel()
	for _, output := range []string{
		`{"type":"stream_event","session_id":"session-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Answer."}}}`,
		`{"type":"stream_event","session_id":"session-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Thought."}}}`,
		string(qoderToolUseFrame("call-1")),
	} {
		t.Run(output, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			for _, delay := range []int{65000, 35000, 5000} {
				a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"api_retry","session_id":"session-1","attempt":1,"max_retries":10,"retry_delay_ms":%d,"error":"rate_limit"}`, delay)))
			}
			a.HandleOutput([]byte(output))
			a.HandleOutput([]byte(output))
			assert.Len(t, sink.PersistedNotifications(), 3)
			assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
		})
	}
}

func TestQoderRetryDoesNotResolveAfterAFailedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","session_id":"session-1"}`))
	a.HandleOutput([]byte(qoderAbortedResult))
	a.HandleOutput(qoderToolUseFrame("next-call"))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestQoderRepeatedInitStoresNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	// Source: .tmp/probe/qoder/run2/logs/frames.jsonl, repeated init frames.
	for range 2 {
		a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"acceptEdits","capabilities":["goal_v1"]}`))
	}
	assert.Equal(t, "session-1", sink.LastSessionID())
	assert.Equal(t, "acceptEdits", a.permissionMode)
	assert.Equal(t, []string{"goal_v1"}, a.capabilities)
	assert.Empty(t, sink.Messages())
}

func TestQoderInstructionWarningPersistsOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	// Source: .tmp/probe/qoder/run2/logs/frames.jsonl, frames 4 and 14.
	raw := []byte(`{"type":"system","subtype":"agents_md_size_warning","session_id":"session-1","total_bytes":40000,"threshold_bytes":32768}`)
	a.HandleOutput(raw)
	a.HandleOutput(raw)
	assert.Empty(t, sink.Messages())
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(raw), string(sink.PersistedNotifications()[0].Content))
}

func TestQoderDroppedSystemSubtypes(t *testing.T) {
	t.Parallel()
	for _, subtype := range []string{"hook_started", "hook_progress", "permission_denied", "elicitation_complete", "artifacts_update", "commands_changed", "control_request_progress", "task_progress", "interrupt", "plan_mode_changed", "goal_updated", "goal_cleared", "available_models_update"} {
		t.Run(subtype, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":%q,"session_id":"session-1"}`, subtype)))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestQoderSuccessfulSessionStartHookStoresNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.HandleOutput([]byte(`{"type":"system","subtype":"hook_response","outcome":"success","hook_event":"SessionStart","session_id":"session-1"}`))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestQoderFailedHookPersists(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	raw := []byte(`{"type":"system","subtype":"hook_response","outcome":"error","hook_event":"SessionStart","exit_code":1,"stderr":"The hook failed.","session_id":"session-1"}`)
	a.HandleOutput(raw)
	assert.Empty(t, sink.Messages())
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(raw), string(sink.PersistedNotifications()[0].Content))
}

func TestQoderDroppedFramesAndUserReplay(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"tool_progress","session_id":"session-1"}`,
		`{"type":"progress","session_id":"session-1"}`,
		`{"type":"command_lifecycle","session_id":"session-1"}`,
		`{"type":"attachment","session_id":"session-1"}`,
		`{"type":"user","session_id":"session-1","message":{"content":[{"type":"text","text":"Replay."}]}}`,
		`{"type":"system","subtype":"task_started","task_type":"local_bash","session_id":"session-1"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			a.HandleOutput([]byte(raw))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestQoderRootStreamEventsStoreNothingAndReportProgress(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	// Source: .tmp/probe/qoder/run3/logs/frames.jsonl, frames 12-25.
	frames := []string{
		`{"type":"message_start"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"First "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"A thought."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_stop"}`,
		`{"type":"retry"}`,
	}
	for _, event := range frames {
		a.HandleOutput([]byte(`{"type":"stream_event","session_id":"session-1","event":` + event + `}`))
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	updates := sink.ProgressUpdates()
	assert.Contains(t, updates, agent.ModelTextProgress("qoder:text:0", "First "))
	assert.Contains(t, updates, agent.ModelTextProgress("qoder:thinking:1", "A thought."))
	assert.Contains(t, updates, agent.CompleteModelProgress("qoder:text:0"))
	assert.Contains(t, updates, agent.CompleteModelProgress("qoder:thinking:1"))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"First answer."}]},"session_id":"session-1"}`))
	a.HandleOutput([]byte(qoderSuccessResult))
	require.Len(t, sink.Messages(), 2)
	assert.True(t, sink.Messages()[1].TurnEnd)
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelProgress())
}

func TestQoderRootStreamInvalidOrEmptyDeltasStayInternal(t *testing.T) {
	t.Parallel()
	for _, event := range []string{
		`null`, `[]`, `"wrong"`,
		`{"type":"content_block_delta","index":-1,"delta":{"type":"text_delta","text":"bad"}}`,
		`{"type":"content_block_delta","index":1.5,"delta":{"type":"text_delta","text":"bad"}}`,
		`{"type":"content_block_delta","index":9223372036854775808,"delta":{"type":"text_delta","text":"bad"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
	} {
		t.Run(event, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			frame, err := json.Marshal(map[string]any{"type": "stream_event", "session_id": "session-1", "event": json.RawMessage(event)})
			require.NoError(t, err)
			a.HandleOutput(frame)
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.ProgressUpdates())
		})
	}
}

func TestQoderRootStreamRejectsInvalidOwnership(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{`false`, `42`, `{}`} {
		t.Run(parent, func(t *testing.T) {
			a, sink, _ := newGoalAgent(t)
			a.HandleOutput([]byte(`{"type":"stream_event","session_id":"session-1","parent_tool_use_id":` + parent + `,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Not root text."}}}`))
			assert.Empty(t, sink.ProgressUpdates())
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestQoderInterruptReplyStoresNoRow(t *testing.T) {
	t.Parallel()
	for _, subtype := range []string{"success", "error"} {
		t.Run(subtype, func(t *testing.T) {
			a, sink, stdin := newGoalAgent(t)
			a.setTurnActive(true)
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			var request struct {
				RequestID string `json:"request_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdin.String()), &request))
			require.NotEmpty(t, request.RequestID)
			raw, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": subtype, "request_id": request.RequestID, "response": map[string]any{}, "error": "The request failed."}})
			require.NoError(t, err)
			a.HandleOutput(raw)
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestQoderControlRequestPublishesWithoutARawRow(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"control_request","request_id":"permission-1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-1","input":{"command":"printf done"}}}`)
	a.HandleOutput(raw)
	require.Equal(t, 1, sink.PublishedControlCount())
	assert.Empty(t, sink.Messages(), "the control banner owns this request")
}

func TestQoderControlCancellationWithdrawsWithoutARawRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"current", `{"type":"control_cancel_request","request_id":"permission-1","session_id":"session-1"}`, []string{"permission-1"}},
		{"no session", `{"type":"control_cancel_request","request_id":"permission-1"}`, []string{"permission-1"}},
		{"another session", `{"type":"control_cancel_request","request_id":"permission-1","session_id":"another"}`, nil},
		{"empty", `{"type":"control_cancel_request","request_id":""}`, nil},
		{"invalid", `{"type":"control_cancel_request","request_id":42}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newGoalAgent(t)
			sink := &agenttest.ControlSink{}
			a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(tc.raw))
			assert.Equal(t, tc.want, sink.CanceledControls())
			assert.Empty(t, sink.Messages())
		})
	}
}

// The frames below are the shapes that qodercli 1.1.65 writes to stdout. The
// usage blocks are left out.
const (
	// qoderAbortedResult ends a turn that an interrupt control request stopped.
	// Qoder uses the same subtype and is_error for a genuine failure.
	qoderAbortedResult = `{"type":"result","subtype":"error_during_execution","duration_ms":2318,"is_error":true,"num_turns":1,"stop_reason":"stop_sequence","total_cost_usd":0,"permission_denials":[],"errors":["Operation aborted"],"uuid":"fbc3efa2-930a-4f9a-a55e-dea1aa56ca28","session_id":"session-1"}`
	// qoderSuccessResult ends a turn that finished. It carries no num_tool_uses.
	qoderSuccessResult = `{"type":"result","subtype":"success","duration_ms":377,"is_error":false,"num_turns":2,"result":"done","stop_reason":"end_turn","total_cost_usd":0,"permission_denials":[],"uuid":"440c942c-6262-444f-841b-5aa4237a7015","session_id":"session-1"}`
)

// qoderToolUseFrame is one main-thread assistant frame that calls one tool.
func qoderToolUseFrame(callID string) []byte {
	return []byte(`{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"` + callID + `","name":"Bash","input":{"command":"echo SOUND42"}}]},"parent_tool_use_id":null,"session_id":"session-1"}`)
}

// qoderToolResultFrame is the user frame that answers one tool call.
func qoderToolResultFrame(callID string) []byte {
	return []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + callID + `","content":"SOUND42"}]},"parent_tool_use_id":null,"session_id":"session-1"}`)
}

// turnEndEntries returns the persisted turn-end rows in order.
func turnEndEntries(sink *agenttest.Sink) []agenttest.Message {
	var ends []agenttest.Message
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			ends = append(ends, message)
		}
	}
	return ends
}

// onlyTurnEnd returns the one persisted turn-end row.
func onlyTurnEnd(t *testing.T, sink *agenttest.Sink) agenttest.Message {
	t.Helper()
	ends := turnEndEntries(sink)
	require.Len(t, ends, 1, "one result frame persists one turn-end row")
	return ends[0]
}

// resolvedTurnEndToolUses reads the count the way the Worker's PersistTurnEnd
// reads it: the provider data first, then the worker metadata over it.
func resolvedTurnEndToolUses(message agenttest.Message) (int32, bool) {
	resolved := agent.ResolveMessageContent(qoderProvider{}, agent.MessageContent{
		Original: message.Content,
		Metadata: message.Metadata,
	})
	return qoderProvider{}.TurnEndToolUses(resolved)
}

// The user stopped the turn, so the turn end must not read as a failure.
func TestQoderInterruptMarksTheAbortedTurnEndInterrupted(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Equal(t, agent.MessageCompletionInterrupted, onlyTurnEnd(t, sink).Completion,
		"the turn that the user stopped must not read as a failure")
}

// A native turn without tools states an explicit zero, so the browser can
// tell "no tools" from "count unknown".
func TestQoderTurnEndReportsExplicitZeroToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok, "a native turn without tools must report explicit zero")
	assert.Equal(t, int32(0), count)
}

func TestQoderTurnEndCountsItsToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	for _, callID := range []string{"call-1", "call-2"} {
		a.HandleOutput(qoderToolUseFrame(callID))
		a.HandleOutput(qoderToolResultFrame(callID))
	}
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok)
	assert.Equal(t, int32(2), count)
}

// The count of one turn must not leak into the next turn. The second turn
// opens the way the real reader opens it: an output frame that arrives while
// the agent is idle.
func TestQoderTurnEndToolUsesStartFromZeroEachTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput(qoderToolUseFrame("call-1"))
	a.HandleOutput(qoderToolResultFrame("call-1"))
	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]},"parent_tool_use_id":null,"session_id":"session-1"}`))
	a.HandleOutput([]byte(qoderSuccessResult))

	ends := turnEndEntries(sink)
	require.Len(t, ends, 2)
	first, ok := resolvedTurnEndToolUses(ends[0])
	assert.True(t, ok)
	assert.Equal(t, int32(1), first)
	second, ok := resolvedTurnEndToolUses(ends[1])
	assert.True(t, ok)
	assert.Equal(t, int32(0), second)
}

// A failed turn that no interrupt stopped keeps the outcome that Qoder states.
func TestQoderFailedTurnWithoutInterruptKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A turn that finished before Qoder read the interrupt keeps its own outcome:
// the stop came too late to change it.
func TestQoderInterruptAfterTheTurnFinishedKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderSuccessResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// Qoder acknowledges an interrupt that arrives while no turn runs, and sends
// no `result` for it. The interrupt must not mislabel the NEXT turn.
func TestQoderIdleInterruptDoesNotMislabelTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newGoalAgent(t)

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Contains(t, stdin.String(), `"subtype":"interrupt"`, "the interrupt still reaches Qoder")
	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A turn can end without a `result`, for example at a stop. The note of that
// turn must not mislabel the next turn.
func TestQoderInterruptNoteEndsWithItsTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.setTurnActive(false)
	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// An interrupt that never reached Qoder stops nothing, so it leaves no note.
func TestQoderUnsentInterruptLeavesNoNote(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.SetStdinForTest(agenttest.FailingStdin{})

	a.setTurnActive(true)
	require.Error(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A child agent's tool calls belong to the child's transcript. Only the
// root's own calls count toward the root turn.
func TestQoderChildToolUsesStayOutOfTheRootCount(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"spawn-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"child-read-1","name":"Read","input":{"file_path":"note.txt"}}]}}`))
	a.HandleOutput(qoderToolUseFrame("root-call-1"))
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok)
	assert.Equal(t, int32(1), count, "the child's Read must not count toward the root turn")
}
