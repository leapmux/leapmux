package codebuddy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestCodebuddyControlResponseReadsStyleMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		payload    string
		hasCatalog bool
		failed     bool
	}{
		{name: "styles with other settings", payload: `{"output_style":"Concise","available_output_styles":["Default","Concise"],"model":"primary","mode":"plan","steered":false,"reason":"idle","availableModels":[{"modelId":"primary"}],"account":{"token":"PRIVATE_ACCOUNT_MARKER"}}`, hasCatalog: true},
		{name: "absent", payload: `{}`},
		{name: "null", payload: `{"available_output_styles":null}`},
		{name: "empty", payload: `{"available_output_styles":[]}`, hasCatalog: true},
		{name: "invalid entry", payload: `{"available_output_styles":[1]}`, failed: true},
		{name: "invalid list", payload: `{"available_output_styles":{}}`, failed: true},
		{name: "invalid current", payload: `{"output_style":false}`, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			reply := make(chan codebuddyControlResult, 1)
			a.pendingControl["init-styles"] = reply
			a.HandleOutput([]byte(codebuddyControlAnswer(tc.payload)("init-styles")))
			select {
			case result := <-reply:
				assert.Equal(t, !tc.failed, result.Success)
				assert.Equal(t, tc.hasCatalog, result.HasOutputStyleCatalog)
				if tc.failed {
					assert.Equal(t, "CodeBuddy sent an invalid control response.", result.Error)
				}
				if tc.name == "styles with other settings" {
					assert.Equal(t, "Concise", result.OutputStyle)
					assert.Equal(t, []string{"Default", "Concise"}, result.OutputStyles)
					assert.Equal(t, "primary", result.Model)
					assert.Equal(t, "plan", result.Mode)
					require.NotNil(t, result.Steered)
					assert.False(t, *result.Steered)
					assert.Equal(t, "idle", result.SteerReason)
					assert.True(t, result.HasModelCatalog)
					assert.Equal(t, []codebuddyModelInfo{{ID: "primary"}}, result.Models)
				}
			default:
				t.Fatal("the native control response was not routed")
			}
			assert.Empty(t, sink.Messages(), "native initialization and account metadata must not enter the transcript")
		})
	}
}

func TestCodebuddyControlResponsePreservesThinkingConfirmation(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		reply := make(chan codebuddyControlResult, 1)
		a.pendingControl["config-1"] = reply
		body, err := json.Marshal(map[string]any{"updated": map[string]bool{"alwaysThinkingEnabled": enabled}, "errors": map[string]string{"other": "Native configuration detail."}})
		require.NoError(t, err)
		a.HandleOutput([]byte(codebuddyControlAnswer(string(body))("config-1")))
		select {
		case result := <-reply:
			require.True(t, result.Success, "outer success does not discard per-key errors")
			require.NotNil(t, result.ConfigUpdated)
			require.NotNil(t, result.ConfigUpdated.AlwaysThinkingEnabled)
			assert.Equal(t, enabled, *result.ConfigUpdated.AlwaysThinkingEnabled)
			assert.Equal(t, map[string]string{"other": "Native configuration detail."}, result.ConfigErrors)
		default:
			t.Fatal("the configuration response was not routed")
		}
		assert.Empty(t, sink.Messages())
	}
}

func TestCodebuddyOnlyRootCompletionRefreshesOutputStyles(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"output_style":"Default","available_output_styles":["Default","Concise","Custom"]}`))
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.outputStyles = []string{"Default", "Concise"}
	a.outputStyle = "Concise"
	openCodebuddyProgressChild(t, a)
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","parent_tool_use_id":"spawn-1","is_error":false}`))
	a.resumeState = &codebuddyResumeState{done: make(chan error, 1)}
	a.HandleOutput([]byte(codebuddyReplayInit))
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","is_error":false}`))
	assert.Empty(t, peer.String(), "child completion and history replay do not refresh root settings")
	a.HandleOutput([]byte(codebuddyResultFinished))
	require.Eventually(t, func() bool { return a.catalogRefresh.Idle() && sink.SettingsRefreshCount() == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, 1, strings.Count(peer.String(), `"subtype":"initialize"`))
	assert.Equal(t, "Concise", a.SettingsSnapshot().SurfacedOptions[OptionOutputStyle])
}

func TestCodebuddyCoalescesOutputStyleDiscovery(t *testing.T) {
	t.Parallel()
	requests := make(chan string, 3)
	a, peer := newCodebuddySteerAgent(t, nil, func(id string) string { requests <- id; return "" })
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.outputStyles = []string{"Default"}
	a.outputStyle = "Default"
	ctx := testutil.DeadlineContext(t)
	nextRequest := func() string {
		t.Helper()
		select {
		case id := <-requests:
			return id
		case <-ctx.Done():
			t.Fatal("the catalog request did not arrive")
			return ""
		}
	}
	a.HandleOutput([]byte(codebuddyResultFinished))
	first := nextRequest()
	for range 4 {
		require.NoError(t, a.SendInput("Another completed turn.", nil))
		a.HandleOutput([]byte(codebuddyResultFinished))
	}
	answer := codebuddyControlAnswer(`{"output_style":"Default","available_output_styles":["Default","Custom"]}`)
	a.HandleOutput([]byte(answer(first)))
	second := nextRequest()
	a.HandleOutput([]byte(answer(second)))
	require.Eventually(t, a.catalogRefresh.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, 2, strings.Count(peer.String(), `"subtype":"initialize"`))
	assert.Empty(t, requests)
	assert.Equal(t, 1, sink.SettingsRefreshCount())
}

type codebuddyTurnReleaseSink struct {
	*agenttest.Sink
	messagesAtRelease []agenttest.Message
	resetsAtRelease   int
}

func TestCodebuddyInitKeepsThePinnedRootSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		session  any
		omit     bool
		matching bool
	}{
		{name: "foreign child", session: "child-session"},
		{name: "empty", session: ""},
		{name: "blank", session: " "},
		{name: "null", session: nil},
		{name: "absent", omit: true},
		{name: "matching root", session: "session-1", matching: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.model = "parent-model"
			a.setTurnActive(true)
			frame := map[string]any{"type": "system", "subtype": "init", "model": "reported-model", "permissionMode": "plan"}
			if !tc.omit {
				frame["session_id"] = tc.session
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			a.HandleOutput(raw)
			assert.Equal(t, "session-1", a.sessionID)
			if tc.matching {
				assert.Equal(t, "reported-model", a.model)
				assert.Equal(t, "plan", a.permissionMode)
				assert.True(t, a.active, "init does not end a turn")
				assert.Equal(t, []string{"session-1"}, sink.SessionIDs())
			} else {
				assert.Equal(t, "parent-model", a.model)
				assert.Equal(t, "default", a.permissionMode)
				assert.True(t, a.active)
				assert.Empty(t, sink.SessionIDs())
			}
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestFrameDrops(t *testing.T) {
	t.Parallel()
	// The probe runs run1 and run3 recorded these administrative frame kinds.
	for _, raw := range []string{
		`{"type":"file-history-snapshot","messageId":"snapshot"}`,
		`{"type":"ai-title","title":"A title"}`,
		`{"type":"topic","topic":"A topic"}`,
		`{"type":"control_notification","notification":{}}`,
		`{"type":"system","subtype":"init","session_id":"session-1","model":"native-model","permissionMode":"plan"}`,
		`{"type":"system","subtype":"keepalive"}`,
		`{"type":"system","subtype":"status","status":null}`,
		`{"type":"system","subtype":"mcp_status","event":"start"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(raw))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	var cases []agenttest.UnknownFrameCase
	for _, session := range []string{"session-1", "", "another-session"} {
		for _, frame := range []map[string]any{
			{"type": "probe_future_frame", "session_id": session},
			{"type": "system", "subtype": "future_notice", "session_id": session},
		} {
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			cases = append(cases, agenttest.UnknownFrameCase{Name: string(raw), Frame: raw, OtherSession: session == "another-session"})
		}
	}
	agenttest.AssertUnknownFramePersists(t, cases, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput(tc.Frame)
		return sink
	})
}

func TestFrameDropsKeepMcpFinishAndUnknownFrames(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"system","subtype":"mcp_status","event":"finish","servers":[]}`,
		`{"type":"system","subtype":"future_notice","message":"retained"}`,
		`{"type":"future_frame","message":"retained"}`,
	} {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(raw))
		require.Len(t, sink.Messages(), 1)
		assert.JSONEq(t, raw, string(sink.Messages()[0].Content))
	}
}

func TestCompactingStatusIsLiveState(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null}`))
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	for range 2 {
		a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	}
	a.HandleOutput([]byte(`{"type":"system","subtype":"status"}`))
	assert.Equal(t, []interface{}{map[string]interface{}{}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	for range 2 {
		a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null}`))
	}
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestManualCompactionStaysLiveUntilItsResult(t *testing.T) {
	for _, result := range []string{
		`{"type":"result","subtype":"success","result":"","is_error":false}`,
		`{"type":"result","subtype":"error_during_execution","result":"","is_error":true,"errors":["summary refused"]}`,
	} {
		t.Run(result, func(t *testing.T) {
			failed := strings.Contains(result, `"is_error":true`)
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			require.NoError(t, a.SendInput("/compact keep the current task", nil))
			assert.False(t, a.PublishTurnActive().Steerable)
			assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, sink.Messages())
			// The stream can clear its previous progress before the manual command starts.
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null}`))
			assert.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction), 1)
			a.HandleOutput([]byte(result))
			assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			// The result stores the boundary row the compaction leaves: the one
			// record of a compaction whose summary drew no row.
			ends := sink.LeapMuxNotifications()
			require.Len(t, ends, 1)
			assert.Equal(t, contracts.NotificationTypeCompactionEnded, ends[0][contracts.NotificationFieldType])
			if failed {
				assert.Equal(t, "failed", ends[0][contracts.NotificationFieldOutcome])
			} else {
				assert.NotContains(t, ends[0], contracts.NotificationFieldOutcome)
			}
		})
	}
}

func TestManualCompactionFailureToWriteEndsLiveState(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.SendInput("/compact", nil))
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.False(t, a.PublishTurnActive().Active)
}

func TestManualCompactionEarlyResultLeavesNoLiveStart(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	// This agent has no pending permission, so the result writes no control frame from this writer.
	a.SetStdinForTest(agenttest.NopStdin(permissionRetiringWriter{output: new(bytes.Buffer), retire: func() {
		a.HandleOutput([]byte(`{"type":"result","subtype":"success","result":"","is_error":false}`))
	}}))
	require.NoError(t, a.SendInput("/compact", nil))
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestOrdinaryInputDoesNotPublishCompaction(t *testing.T) {
	for _, input := range []string{"", "/compactly", "explain /compact"} {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		require.NoError(t, a.SendInput(input, nil))
		assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	}
}

func (s *codebuddyTurnReleaseSink) SetTurnState(state agent.TurnState, seq uint64) {
	if !state.Active {
		s.messagesAtRelease = s.Messages()
		s.resetsAtRelease = s.ResetSpanCount()
	}
	s.Sink.SetTurnState(state, seq)
}

func TestCodebuddyTurnEndRecordsExplicitZeroBeforeQueueRelease(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	result := []byte(`{"type":"result","subtype":"success","result":"","is_error":false}`)
	a.HandleOutput(result)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.True(t, messages[0].TurnEnd)
	assert.Equal(t, result, messages[0].Content)
	assert.Equal(t, []int{0}, agenttest.TurnToolUseCounts(t, messages))
	assert.Equal(t, []string{"turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestCodebuddyTurnEndCountsUniqueRootCallsAndResetsEachTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	request := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"first","name":"Read"},{"type":"tool_use","id":"second","name":"Bash"},{"type":"tool_use","id":"first","name":"Read"},{"type":"tool_use","id":"","name":"Bash"},{"type":"tool_use","id":" ","name":"Bash"}]}}`)
	a.HandleOutput(request)
	a.HandleOutput(request)
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"unknown-child","message":{"content":[{"type":"tool_use","id":"child-call","name":"Bash"}]}}`))
	result := []byte(`{"type":"result","subtype":"success","result":"","is_error":false}`)
	a.HandleOutput(result)
	a.HandleOutput(result)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"first","name":"Read"}]}}`))
	a.HandleOutput(result)
	assert.Equal(t, []int{2, 0, 1}, agenttest.TurnToolUseCounts(t, sink.Messages()))
}

// These `result` frames are real. CodeBuddy Code 2.160.0 wrote them in a probe against a
// local mock model. Each frame keeps the fields that state its outcome and omits the
// usage objects.
//
// CodeBuddy ends a stop with the SUCCESS shape: `subtype: success`, `is_error: false`.
// Only `terminal_reason` (CodeBuddy 2.158.0 and later) states that the stop took effect.
// A rule that reads a `result` with `is_error: false` as a finished turn, which
// is right for Claude Code and Qoder, would read each stop of CodeBuddy as a finished turn.
const (
	// The stop took effect while the model streamed.
	codebuddyResultAbortedStreaming = `{"type":"result","subtype":"success","is_error":false,"result":"","duration_ms":4277,"num_turns":2,"terminal_reason":"aborted_streaming"}`
	// The stop took effect while a tool ran.
	codebuddyResultAbortedTools = `{"type":"result","subtype":"success","is_error":false,"result":"","duration_ms":5621,"num_turns":3,"terminal_reason":"aborted_tools"}`
	// The turn finished before the CLI read the stop. The probe sent the stop right
	// after the answer frame, and the CLI still ended the turn with this frame.
	codebuddyResultFinished = `{"type":"result","subtype":"success","is_error":false,"result":"QUICK ANSWER","duration_ms":702,"num_turns":2}`
	// The turn failed on its own before the CLI read the stop.
	codebuddyResultFailed = `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":102,"num_turns":2,"errors":["400 NATIVE ERROR MARKER (01a108db06177af38a010285fbc74cdf/e46a2031-5536-4038-bfe6-ad97284f4a24)"]}`
)

func TestUnclaimedControlResponseHasOneVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw, errorText string
		unknown              bool
	}{
		// e2e-B03pI0 recorded this acknowledgement after the native interrupt.
		{"success", `{"type":"control_response","response":{"subtype":"success","request_id":"interrupt-1","response":{"session_id":"session","interrupted":true,"reason":"user"}}}`, "", false},
		{"failure", `{"type":"control_response","response":{"subtype":"error","request_id":"interrupt-1","error":"The interrupt failed."}}`, "The interrupt failed.", false},
		{"empty failure", `{"type":"control_response","response":{"subtype":"error","request_id":"interrupt-1","error":" "}}`, "CodeBuddy refused a control request.", false},
		{"absent identity", `{"type":"control_response","response":{"subtype":"success"}}`, "", true},
		{"unknown subtype", `{"type":"control_response","response":{"subtype":"future","request_id":"interrupt-1","detail":true}}`, "", true},
		{"invalid response", `{"type":"control_response","response":"future shape"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(tc.raw))
			if tc.unknown {
				require.Len(t, sink.Messages(), 1)
				assert.JSONEq(t, tc.raw, string(sink.Messages()[0].Content))
			} else {
				assert.Empty(t, sink.Messages())
			}
			if tc.errorText != "" {
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, contracts.NotificationTypeAgentError, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
				assert.Equal(t, tc.errorText, sink.LeapMuxNotifications()[0][contracts.NotificationFieldError])
			} else {
				assert.Empty(t, sink.LeapMuxNotifications())
			}
		})
	}
}

func TestCodebuddyInterruptMarksOnlyTheCurrentNativeResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	require.NoError(t, a.SendInput("Run the current turn.", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	result := []byte(codebuddyResultAbortedStreaming)
	a.HandleOutput(result)
	first := sink.Messages()
	require.Len(t, first, 1)
	assert.Equal(t, result, first[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, first[0].Completion)
	require.NoError(t, a.SendInput("Run the next turn.", nil))
	a.HandleOutput(result)
	final := sink.Messages()
	require.Len(t, final, 2)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, final[1].Completion)
}

// turnEnds returns the turn-end rows of the sink. A `result` that carries an answer
// also stores that answer as an assistant row before its turn end (see
// backfillMissingAssistantText), so a test cannot count the rows of the sink.
func turnEnds(messages []agenttest.Message) []agenttest.Message {
	var rows []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			rows = append(rows, message)
		}
	}
	return rows
}

// A stop request marks the turn end as interrupted only when the `result` states that
// the stop took effect. CodeBuddy states it in `terminal_reason`.
func TestCodebuddyAStopThatTookEffectMarksTheTurnEndInterrupted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, frame string }{
		{"aborted while the model streamed", codebuddyResultAbortedStreaming},
		{"aborted while a tool ran", codebuddyResultAbortedTools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			require.NoError(t, a.SendInput("Run the current turn.", nil))
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			a.HandleOutput([]byte(tc.frame))
			rows := turnEnds(sink.Messages())
			require.Len(t, rows, 1)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
		})
	}
}

// A turn that ended before the CLI read the stop keeps its own outcome, because the
// stop came too late to change it. Before this rule, the note of the stop marked each
// `result`, and the divider read "Turn interrupted" for a turn that finished or failed
// on its own.
func TestCodebuddyATurnThatEndedBeforeTheStopKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, frame string }{
		{"finished", codebuddyResultFinished},
		{"failed on its own", codebuddyResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			require.NoError(t, a.SendInput("Run the current turn.", nil))
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			a.HandleOutput([]byte(tc.frame))
			rows := turnEnds(sink.Messages())
			require.Len(t, rows, 1)
			assert.NotEqual(t, agent.MessageCompletionInterrupted, rows[0].Completion,
				"the stop lost the race, so the turn end keeps the outcome that the frame states")
		})
	}
}

// A finished turn spends the note, as any other `result` does. The abort frame of
// the next turn does not mark that turn unless the user asks for a stop during it.
func TestCodebuddyAFinishedTurnSpendsTheStopNote(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	require.NoError(t, a.SendInput("Run the current turn.", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(codebuddyResultFinished))
	require.NoError(t, a.SendInput("Run the next turn.", nil))
	a.HandleOutput([]byte(codebuddyResultAbortedStreaming))
	rows := turnEnds(sink.Messages())
	require.Len(t, rows, 2)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestCodebuddyIdleInterruptDoesNotMarkTheNextNativeResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, a.SendInput("Run a new turn.", nil))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","result":"","is_error":false}`))
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, messages[0].Completion)
}

func TestCodebuddyTurnEndPersistsResultBeforeReleasingQueuedInput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"result","session_id":"native-session","result":"Finished.","is_error":false}`,
		`{"type":"result","session_id":"native-session","usage":false}`,
	} {
		t.Run(raw, func(t *testing.T) {
			sink := &codebuddyTurnReleaseSink{Sink: &agenttest.Sink{}}
			a := newOfflineAgent(t, sink.Sink)
			a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"pending","name":"REPL"}]}}`))
			a.HandleOutput([]byte(raw))
			require.GreaterOrEqual(t, len(sink.messagesAtRelease), 2)
			assert.Equal(t, raw, string(sink.messagesAtRelease[len(sink.messagesAtRelease)-1].Content))
			assert.Equal(t, 1, sink.resetsAtRelease)
		})
	}
}

func TestCodebuddyLiveReplFramesPersistTheirExactToolSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	request := []byte(`{"type":"assistant","session_id":"native-session","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"native-code-1","name":"REPL","input":{"code":"throw new Error(\"computed-\" + (70 + 7));"}}]}}`)
	result := []byte(`{"type":"user","session_id":"native-session","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"native-code-1","is_error":false,"content":[{"type":"text","text":"{\"stdout\":\"\",\"stderr\":\"\",\"error\":\"computed-77\"}"}]}]}}`)
	a.HandleOutput(request)
	a.HandleOutput(result)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	for _, message := range messages {
		assert.Equal(t, "native-code-1", message.SpanID)
		assert.Equal(t, "REPL", message.SpanType)
	}
	assert.True(t, bytes.Equal(request, messages[0].Content), "the request retains its native bytes")
	assert.True(t, bytes.Equal(result, messages[1].Content), "the result retains its native bytes")
	assert.False(t, messages[0].Closing)
	assert.True(t, messages[1].Closing)
	assert.Empty(t, messages[0].SpansOpenAtPersist)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "native-code-1"}}, messages[1].SpansOpenAtPersist)
}

func TestCodebuddyLiveToolSpansHandleMixedAndMultipleBlocks(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Read two files."},{"type":"tool_use","id":"first","name":"Read"},{"type":"tool_use","id":"second","name":"REPL"}]}}`))
	assert.Equal(t, "Read", sink.GetSpanType("first"))
	assert.Equal(t, "REPL", sink.GetSpanType("second"))
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "first"}, {SpanID: "second"}}, sink.OpenSpans())
	a.HandleOutput([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"Unrelated text."},{"type":"tool_result","tool_use_id":"first","content":"one"},{"type":"tool_result","tool_use_id":"second","content":"two"}]}}`))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, "first", messages[0].SpanID)
	assert.Equal(t, "first", messages[1].SpanID)
	assert.Equal(t, "Read", messages[1].SpanType)
	assert.Equal(t, []string{"first", "second"}, sink.ClosedSpans())
}

func TestCodebuddyLiveToolSpansIgnoreInvalidIDsAndKeepUnknownResultIdentity(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"","name":"REPL"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":" ","name":"REPL"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":""}]}}`,
		`{"type":"user","message":{"content":"malformed result blocks"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(raw))
			messages := sink.Messages()
			require.Len(t, messages, 1)
			assert.Empty(t, messages[0].SpanID)
			assert.Empty(t, sink.OpenSpans())
			assert.Empty(t, sink.ClosedSpans())
		})
	}
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"unknown","content":"native output"}]}}`))
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, "unknown", messages[0].SpanID)
	assert.Empty(t, messages[0].SpanType)
}

func TestCodebuddyFailedPersistLeavesToolSpanLifetimeUnchanged(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{PersistErr: errors.New("The native row write failed.")}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"failed","name":"REPL"}]}}`))
	assert.Empty(t, sink.OpenSpans())
	assert.Empty(t, sink.GetSpanType("failed"))
	sink.PersistErr = nil
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"kept","name":"REPL"}]}}`))
	sink.PersistErr = errors.New("The native result write failed.")
	a.HandleOutput([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"kept","content":"output"}]}}`))
	assert.Empty(t, sink.ClosedSpans())
	assert.Equal(t, "REPL", sink.GetSpanType("kept"))
}

func TestCodebuddyTurnEndResetsOpenSpansBeforeReleasingQueuedInput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"result","session_id":"native-session","result":"Finished.","is_error":false}`,
		`{"type":"result","session_id":"native-session","usage":false}`,
	} {
		t.Run(raw, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"pending","name":"REPL"}]}}`))
			require.Equal(t, "REPL", sink.GetSpanType("pending"))
			a.HandleOutput([]byte(raw))
			assert.Equal(t, 1, sink.ResetSpanCount())
			assert.Empty(t, sink.GetSpanType("pending"))
			lifecycle := sink.TurnLifecycle()
			resetIndex := -1
			clearIndex := -1
			for index, event := range lifecycle {
				if event == "reset_spans" {
					resetIndex = index
				}
				if event == "turn_active:false" {
					clearIndex = index
				}
			}
			require.NotEqual(t, -1, resetIndex)
			require.NotEqual(t, -1, clearIndex)
			assert.Less(t, resetIndex, clearIndex)
		})
	}
}

func TestCodebuddyMalformedMixedFramesDoNotChangeSpanLifetime(t *testing.T) {
	t.Parallel()
	t.Run("request with a malformed later block", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"partial","name":"REPL"},{"type":"tool_use","id":7,"name":"Read"}]}}`))
		messages := sink.Messages()
		require.Len(t, messages, 1)
		assert.Empty(t, messages[0].SpanID)
		assert.Empty(t, sink.OpenSpans())
		assert.Empty(t, sink.GetSpanType("partial"))
	})
	t.Run("result with a malformed later block", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"kept","name":"REPL"}]}}`))
		a.HandleOutput([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"kept","content":"output"},{"type":"tool_result","tool_use_id":false}]}}`))
		messages := sink.Messages()
		require.Len(t, messages, 2)
		assert.Empty(t, messages[1].SpanID)
		assert.Empty(t, sink.ClosedSpans())
		assert.Equal(t, "REPL", sink.GetSpanType("kept"))
	})
}

func TestCodebuddyResultBackfillsAnAnswerThatReasoningDidNotStream(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I inspect the numbers first."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","result":"The answer is 6,912.","is_error":false}`))

	messages := sink.Messages()
	require.Len(t, messages, 3, "the missing answer appears between the native thinking and result frames")
	var answer struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(messages[1].Content, &answer))
	assert.Equal(t, contracts.CodebuddyFrameKindAssistant, answer.Type)
	assert.Equal(t, "assistant", answer.Message.Role)
	require.Len(t, answer.Message.Content, 1)
	assert.Equal(t, "text", answer.Message.Content[0].Type)
	assert.Equal(t, "The answer is 6,912.", answer.Message.Content[0].Text)
	assert.Contains(t, string(messages[2].Content), `"type":"result"`, "the native result still ends the turn")
}

func TestCodebuddyEnterPlanModeUpdatesOnlyAfterSuccessfulNativeResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "successful result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}],"_meta":{"rawResponse":{"renderer":{"type":"enterplan"}}}}]}}`,
			want:   contracts.CodebuddyModePlan,
		},
		{
			name:   "failed result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","is_error":true,"content":[{"type":"text","text":"Plan mode failed."}]}]}}`,
		},
		{
			name:   "unrelated result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"other-tool","is_error":false,"content":[{"type":"text","text":"Done."}]}]}}`,
		},
		{
			name:   "result without status",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","content":[{"type":"text","text":"Unknown outcome."}]}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"enter-plan","name":"EnterPlanMode","input":{}}]}}`))
			assert.Empty(t, sink.PermissionModes(), "a tool request does not confirm a mode change")
			a.HandleOutput([]byte(tc.result))
			assert.Equal(t, tc.want, sink.PermissionMode())
			if tc.want != "" {
				assert.Equal(t, tc.want, a.permissionMode)
			} else {
				assert.Equal(t, "default", a.permissionMode)
			}
		})
	}
}

func TestCodebuddyNativePlanToolRequestsASessionPreservingRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frames []string
		want   bool
	}{
		{
			name: "successful plan entry persists through turn end",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}]}]}}`,
				`{"type":"result","result":"Ready to plan.","is_error":false}`,
			},
			want: true,
		},
		{
			name: "rejected plan exit persists through the next init",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. Keep planning."}]}]}}`,
				`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`,
			},
			want: true,
		},
		{
			name: "approved plan exit requires its new launch mode",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"The plan is approved."}]}]}}`,
				`{"type":"result","result":"Implementing the plan.","is_error":false}`,
			},
			want: true,
		},
		{
			name: "failed plan entry does not request restart",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Could not enter plan mode."}]}]}}`,
				`{"type":"result","result":"Plan failed.","is_error":true}`,
			},
		},
		{
			name:   "native init without a plan tool does not request restart",
			frames: []string{`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			if tc.name == "approved plan exit requires its new launch mode" {
				a.permissionMode = contracts.CodebuddyModePlan
				a.opts = agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModePlan}}
			}
			a.setTurnActive(true)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			restarter, ok := any(a).(interface{ NativeTurnRestartRequired() bool })
			require.True(t, ok, "CodeBuddy must report tool-driven mode changes before the next queued input")
			assert.Equal(t, tc.want, restarter.NativeTurnRestartRequired())
		})
	}
}

func TestCodebuddyNativePlanToolRestartWithMatchingLaunchMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{
			name: "plan entry",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}]}]}}`,
				`{"type":"result","result":"Ready to plan.","is_error":false}`,
			},
		},
		{
			name: "rejected exit",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. Keep planning."}]}]}}`,
				`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.opts = agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModePlan}}
			a.permissionMode = contracts.CodebuddyModePlan
			a.setTurnActive(true)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			assert.Equal(t, tc.name == "rejected exit", a.NativeTurnRestartRequired(), "rejection requires restoring the native input loop even when launch mode matches")
		})
	}
}

func TestCodebuddyInitPublishesNativePermissionMode(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
	assert.Equal(t, contracts.CodebuddyModePlan, a.permissionMode)
	assert.Equal(t, contracts.CodebuddyModePlan, sink.PermissionMode())

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1"}`))
	assert.Equal(t, contracts.CodebuddyModePlan, a.permissionMode, "an absent mode leaves the last native state alone")
	assert.Equal(t, []string{contracts.CodebuddyModePlan}, sink.PermissionModes())

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"acceptEdits"}`))
	assert.Equal(t, contracts.CodebuddyModeAcceptEdits, a.permissionMode)
	assert.Equal(t, []string{contracts.CodebuddyModePlan, contracts.CodebuddyModeAcceptEdits}, sink.PermissionModes())
}

func TestCodebuddyRejectedPlanExitSettlesAfterItsResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		tool       string
		result     string
		wantActive bool
	}{
		{
			name:   "denied plan exit",
			tool:   "ExitPlanMode",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. They want to keep planning."}]}]}}`,
		},
		{
			name:       "failed plan exit",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Could not read the plan file."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "approved plan exit",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"The plan is approved."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "unrelated tool failure",
			tool:       "Bash",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Command failed."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "plan result with no outcome",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","content":[{"type":"text","text":"Unknown outcome."}]}]}}`,
			wantActive: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &codebuddyTurnReleaseSink{Sink: &agenttest.Sink{}}
			a := newOfflineAgent(t, sink.Sink)
			a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
			use := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"` + tc.tool + `","input":{}}]}}`
			a.HandleOutput([]byte(use))
			a.HandleOutput([]byte(tc.result))
			active, published := sink.LastTurnActive()
			assert.True(t, published)
			assert.Equal(t, tc.wantActive, active)
			if !tc.wantActive {
				require.Len(t, sink.messagesAtRelease, 2)
				assert.JSONEq(t, tc.result, string(sink.messagesAtRelease[1].Content))
				assert.Equal(t, contracts.CodebuddyModePlan, sink.PermissionMode())
				assert.True(t, a.NativeTurnRestartRequired())
			}

			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
			active, published = sink.LastTurnActive()
			assert.True(t, published)
			assert.Equal(t, tc.wantActive, active)
			if !tc.wantActive {
				require.NoError(t, a.SendInput("Present the revised plan.", nil))
			}
		})
	}
}

func TestCodebuddyInitDoesNotRepeatRejectedPlanCompletion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet."}]}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"","is_error":false}`))
	states := sink.TurnActives()
	require.NotEmpty(t, states)
	assert.False(t, states[len(states)-1])

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
	assert.Equal(t, states, sink.TurnActives(), "a later init does not publish a second end for the old turn")
}

func TestCodebuddyResultDoesNotDuplicateStreamedText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The answer is 6,912."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"The answer is 6,912.","is_error":false}`))

	messages := sink.Messages()
	require.Len(t, messages, 2, "a streamed answer needs no second assistant row")
	assert.Contains(t, string(messages[0].Content), `"type":"text"`)
	assert.Contains(t, string(messages[1].Content), `"type":"result"`)
}

func TestCodebuddyResultBackfillsAfterAPriorTurnStreamedText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"First answer."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"First answer."}`))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Second thought."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"Second answer."}`))

	messages := sink.Messages()
	require.Len(t, messages, 5)
	assert.Contains(t, string(messages[3].Content), `"text":"Second answer."`)
	assert.Contains(t, string(messages[4].Content), `"type":"result"`)
}

func TestCodebuddyResultDoesNotBackfillAnErrorOrEmptyAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result string
	}{
		{name: "error", result: `{"type":"result","result":"The request failed.","is_error":true}`},
		{name: "blank", result: `{"type":"result","result":" ","is_error":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Thinking."}]}}`))
			a.HandleOutput([]byte(tc.result))
			assert.Len(t, sink.Messages(), 2)
		})
	}
}

func TestCodebuddyAvailableModelsResponseKeepsNativeIds(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	response := make(chan codebuddyControlResult, 1)
	a.pendingControl["models-1"] = response
	line := providerkit.ParseLine([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"models-1","response":{"availableModels":[{"modelId":"custom-local:primary","name":"Primary"},{"modelId":"custom-local:alternate","name":"Alternate"}]}}}`))

	require.True(t, a.handlePendingControlResponse(line))
	result := <-response
	require.True(t, result.Success)
	require.True(t, result.HasModelCatalog)
	assert.Equal(t, []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}, result.Models)
}

func TestCodebuddyResultReportsPrimaryModelContext(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.model = "custom-local:primary"
	a.handleResult([]byte(`{
      "type":"result",
      "usage":{"input_tokens":13000,"output_tokens":50},
      "modelUsage":{
        "custom-local:primary":{"inputTokens":12000,"outputTokens":40,"contextWindow":128000},
        "custom-local:child":{"inputTokens":1000,"outputTokens":10,"contextWindow":64000}
      },
      "_meta":{"codebuddy.ai/contextUsed":12000}
    }`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(12000),
		contracts.ContextUsageFieldOutputTokens:             int64(40),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(0),
		contracts.ContextUsageFieldContextTokens:            int64(12000),
		contracts.ContextUsageFieldContextWindow:            int64(128000),
	}, value)
}

func TestCodebuddyResultUsesAggregateCountsWithoutAModelMatch(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.model = "custom-local:primary"
	a.handleResult([]byte(`{
      "type":"result",
      "usage":{"input_tokens":25,"output_tokens":4,"cache_read_input_tokens":7},
      "modelUsage":{
        "custom-local:secondary":{"inputTokens":10,"contextWindow":32000},
        "custom-local:child":{"inputTokens":15,"contextWindow":16000}
      }
    }`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(25),
		contracts.ContextUsageFieldOutputTokens:             int64(4),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(7),
	}, value)
}

func TestCodebuddyResultKeepsAnExplicitZeroContext(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.handleResult([]byte(`{"type":"result","usage":{"input_tokens":25},"_meta":{"codebuddy.ai/contextUsed":0}}`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldContextTokens])
}

func TestCodebuddyResultWithoutValidUsageDoesNotBroadcast(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.handleResult([]byte(`{"type":"result"}`))
	a.handleResult([]byte(`{"type":"result","usage":"malformed"}`))

	_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	assert.False(t, ok)
}

func TestCodebuddyRejectedPlanResultOwnershipAndReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "foreign session", field: "session_id", value: "other-session"},
		{name: "child result", field: "parent_tool_use_id", value: "child-tool"},
		{name: "unknown call", field: "tool_use_id", value: "other-call"},
		{name: "matching root", field: "session_id", value: "session-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &codebuddyTurnReleaseSink{Sink: &agenttest.Sink{}}
			a := newOfflineAgent(t, sink.Sink)
			a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`))
			block := map[string]any{"type": "tool_result", "tool_use_id": "plan-call", "is_error": true, "content": []any{map[string]any{"type": "text", "text": codebuddyPlanRejectionPrefix}}}
			frame := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{block}}}
			if tc.field == "tool_use_id" {
				block[tc.field] = tc.value
			} else {
				frame[tc.field] = tc.value
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			a.HandleOutput(raw)
			if tc.name != "matching root" {
				assert.True(t, a.active)
				assert.False(t, a.NativeTurnRestartRequired())
				assert.Empty(t, sink.PermissionModes())
				return
			}
			assert.False(t, a.active)
			assert.True(t, a.NativeTurnRestartRequired())
			states := sink.TurnActives()
			a.HandleOutput(raw)
			assert.Equal(t, states, sink.TurnActives(), "a duplicate result does not publish another end")
			require.NoError(t, a.SendInput("A new turn", nil))
			a.HandleOutput(raw)
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
			assert.True(t, a.active, "old rejection and init frames cannot end the new turn")
		})
	}
}

func TestCodebuddyPlanExitRestoresNativeMode(t *testing.T) {
	t.Parallel()
	for _, initial := range []string{contracts.CodebuddyModeDefault, contracts.CodebuddyModeAcceptEdits, contracts.CodebuddyModeBypassPermissions, contracts.CodebuddyModePlan} {
		t.Run(initial, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.permissionMode = initial
			a.opts = agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: initial}}
			tool := func(id, name string, success bool) {
				use, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}}}})
				require.NoError(t, err)
				a.HandleOutput(use)
				result, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": !success, "content": []any{map[string]any{"type": "text", "text": "Native tool result."}}}}}})
				require.NoError(t, err)
				a.HandleOutput(result)
			}
			tool("enter", "EnterPlanMode", true)
			tool("enter-again", "EnterPlanMode", true)
			tool("exit-failed", "ExitPlanMode", false)
			assert.Equal(t, contracts.CodebuddyModePlan, a.permissionMode)
			tool("exit", "ExitPlanMode", true)
			want := initial
			if initial == contracts.CodebuddyModePlan {
				want = contracts.CodebuddyModeDefault
			}
			assert.Equal(t, want, a.permissionMode)
			assert.Equal(t, want, sink.PermissionMode())
			tool("exit-again", "ExitPlanMode", true)
			assert.Equal(t, want, a.permissionMode, "an exit outside Plan preserves its current mode")
		})
	}
}
