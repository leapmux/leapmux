package droid

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

type droidFailNotificationSink struct {
	*agenttest.Sink
	fail bool
}

type droidRetryRecordingSink struct {
	*agenttest.Sink
	events []string
}

func (sink *droidRetryRecordingSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	stored, err := sink.Sink.PersistNotification(source, content)
	if err == nil && stored {
		sink.events = append(sink.events, "native")
	}
	return stored, err
}

func (sink *droidRetryRecordingSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	stored, err := sink.Sink.PersistWorkerNotification(content)
	if err == nil && stored {
		sink.events = append(sink.events, "worker")
	}
	return stored, err
}

func (sink *droidRetryRecordingSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	err := sink.Sink.PersistMessage(source, content, span)
	if err == nil {
		sink.events = append(sink.events, "message")
	}
	return err
}

func (sink *droidFailNotificationSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if sink.fail {
		return false, assert.AnError
	}
	return sink.Sink.PersistNotification(source, content)
}

func TestNotificationVerdicts(t *testing.T) {
	t.Parallel()
	// These shapes come from .tmp/probe/droid/run1 and run2. The tests omit identifiers that do not affect the verdict.
	for _, tc := range []struct {
		name    string
		payload string
		stored  bool
	}{
		{"heartbeat", `{"type":"tool_execution_heartbeat","toolUseId":"call"}`, false},
		{"permission receipt", `{"type":"permission_resolved"}`, false},
		{"token usage", `{"type":"session_token_usage_changed","tokenUsage":{"total":7}}`, false},
		{"title", `{"type":"session_title_updated","title":"new title"}`, false},
		{"hook start", `{"type":"hook_execution_started","hookEventName":"UserPromptSubmit"}`, false},
		{"hook success", `{"type":"hook_execution_completed","hookStatus":"completed","hookResults":[{"exitCode":0}]}`, false},
		{"hidden hook failure", `{"type":"hook_execution_completed","hookStatus":"error","hiddenFromUserViews":true}`, false},
		{"hook error", `{"type":"hook_execution_completed","hookStatus":"error","hookResults":[{"exitCode":2,"stderr":"refused"}]}`, true},
		{"hook refusal", `{"type":"hook_execution_completed","hookPreventedAction":true}`, true},
		{"structured output", `{"type":"structured_output","data":{"value":7}}`, false},
		{"model context", `{"type":"create_message","message":{"id":"context","role":"user","visibility":"llm_only","content":[]}}`, false},
		{"tool phase", `{"type":"tool_execution_phase_changed","toolUseId":"call","phase":"running"}`, false},
		{"working state", `{"type":"droid_working_state_changed","newState":"thinking"}`, false},
		{"unknown", `{"type":"future_notification","value":7}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "", []byte(tc.payload)))
			if tc.stored {
				require.Len(t, sink.PersistedNotifications(), 1)
				assert.JSONEq(t, tc.payload, string(sink.PersistedNotifications()[0].Content))
			} else {
				assert.Empty(t, sink.PersistedNotifications())
				assert.Empty(t, sink.Messages())
			}
		})
	}
}

func TestMcpStatusStartupStoresNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.sessionID = ""
	// The first frame of .tmp/probe/droid/run1 has no session identity.
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"mcp_status_changed","servers":[],"summary":{"total":0,"connected":0,"failed":0}}`)))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
}

func TestMcpStatusAuthFailureStoresOneNotice(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	// .tmp/probe/droid/run6 reports the failure before it adds requiresAuth.
	for _, payload := range []string{
		`{"type":"mcp_status_changed","servers":[{"name":"probe-oauth","status":"connecting"}]}`,
		`{"type":"mcp_status_changed","servers":[{"name":"probe-oauth","status":"failed","error":"Authentication required"}]}`,
		`{"type":"mcp_status_changed","servers":[{"name":"probe-oauth","status":"failed","requiresAuth":true,"error":"Authentication required"}]}`,
	} {
		a.HandleOutput(droidAuditNotification(t, "", []byte(payload)))
	}
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "Authentication required")
	assert.Equal(t, agent.ProviderScopedNotification("droid:mcp_status"), (droidProvider{}).Classify(sink.PersistedNotifications()[0].Content))
}

func TestUnknownNotificationOfAnotherSessionIsLogged(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "another-session", []byte(`{"type":"future_notification","value":7}`)))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
}

func TestUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"type":"new_native_notice","detail":"retain this"}`)
	var cases []agenttest.UnknownFrameCase
	for _, session := range []string{"main-session", "", "another-session"} {
		cases = append(cases, agenttest.UnknownFrameCase{
			Name: session, Frame: droidAuditNotification(t, session, payload), Stored: payload,
			OtherSession: session == "another-session",
		})
	}
	agenttest.AssertUnknownFramePersists(t, cases, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink, _ := newSteerAgent(t)
		a.HandleOutput(tc.Frame)
		return sink
	})
}

func TestUserEchoStoresNoDuplicateRow(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			fixture := newTestAgent(t)
			a := fixture.agent
			target, ok := a.outputTargetFor("")
			require.True(t, ok)
			if child {
				target.childSessionID = "native-child"
				target.sink = fixture.sink.ChildSink("stored-child")
			}
			a.onCreateMessage([]byte(`{"type":"create_message","message":{"id":"native-user","role":"user","content":[{"type":"text","text":"already stored input"}]}}`), target)
			assert.Empty(t, fixture.sink.Messages())
			assert.Empty(t, fixture.sink.Child("stored-child").Messages())
		})
	}
}

func TestMcpStatusComparesFailureSetsAndKeepsChangedErrors(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, payload := range []string{
		`{"type":"mcp_status_changed","servers":[{"name":"a","status":"failed","error":"first"},{"name":"b","requiresAuth":true}]}`,
		`{"type":"mcp_status_changed","servers":[{"name":"b","requiresAuth":true},{"name":"a","status":"failed","error":"first"},{"name":"a","status":"failed","error":"first"}]}`,
		`{"type":"mcp_status_changed","servers":[{"name":"a","status":"failed","error":"changed"},{"name":"b","requiresAuth":true}]}`,
		`{"type":"mcp_status_changed","servers":[{"name":"a","status":"connected"},{"name":"b","status":"disabled"}]}`,
	} {
		a.HandleOutput(droidAuditNotification(t, "", []byte(payload)))
	}
	require.Len(t, sink.PersistedNotifications(), 2)
	assert.Contains(t, string(sink.PersistedNotifications()[1].Content), "changed")
}

func TestMcpStatusKeepsItsRootCacheAcrossStartup(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.sessionID = ""
	payload := []byte(`{"type":"mcp_status_changed","servers":[{"name":"a","status":"failed","error":"unavailable"}]}`)
	a.HandleOutput(droidAuditNotification(t, "", payload))
	a.Mu.Lock()
	a.sessionID = "native-session"
	a.Mu.Unlock()
	a.HandleOutput(droidAuditNotification(t, "native-session", payload))
	require.Len(t, sink.PersistedNotifications(), 1)
}

func TestMcpStatusKeepsOnePendingNoticeAfterAWriteFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	payload := []byte(`{"type":"mcp_status_changed","servers":[{"name":"a","status":"failed","error":"unavailable"}]}`)
	writer := &droidFailNotificationSink{Sink: sink, fail: true}
	a.sink = agent.NewProviderServices(writer)
	a.HandleOutput(droidAuditNotification(t, "", payload))
	a.HandleOutput(droidAuditNotification(t, "", payload))
	assert.Empty(t, sink.PersistedNotifications())
	writer.fail = false
	a.HandleOutput(droidAuditNotification(t, "", payload))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(payload), string(sink.PersistedNotifications()[0].Content))
}

func TestMcpStatusAndSettingsKeepTheirChildOwnership(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	payload := []byte(`{"type":"mcp_status_changed","servers":[{"name":"a","status":"failed"}]}`)
	a.HandleOutput(droidAuditNotification(t, "main-session", payload))
	a.HandleOutput(droidAuditNotification(t, "child-1", payload))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"settings_updated","settings":{"modelId":"child-model"}}`)))
	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.Child(row.ChildAgentID).PersistedNotifications(), 1)
}

func TestHookCompletedErrorPersists(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	payload := []byte(`{"type":"hook_execution_completed","hookStatus":"error","hookEventName":"UserPromptSubmit","hookResults":[{"exitCode":2,"stderr":"refused"}]}`)
	writer := &droidFailNotificationSink{Sink: sink, fail: true}
	a.sink = agent.NewProviderServices(writer)
	a.HandleOutput(droidAuditNotification(t, "", payload))
	assert.Empty(t, sink.PersistedNotifications())
	writer.fail = false
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"hook_execution_started"}`)))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(payload), string(sink.PersistedNotifications()[0].Content))
}

func TestToolProgressUpdateReportsATailAndStoresNoRow(t *testing.T) {
	t.Parallel()
	// The update shape comes from .tmp/probe/droid/run3, frames 20 through 29.
	for _, tc := range []struct {
		name      string
		update    map[string]any
		tail      string
		truncated bool
	}{
		{"text", map[string]any{"text": "partial"}, "partial", false},
		{"full output", map[string]any{"text": "delta", "fullOutput": "whole output"}, "whole output", false},
		{"empty full output", map[string]any{"text": "old", "fullOutput": ""}, "", false},
		{"null full output", map[string]any{"text": "fallback", "fullOutput": nil}, "fallback", false},
		{"absent text", map[string]any{}, "", false},
		{"below limit", map[string]any{"fullOutput": strings.Repeat("x", 8191)}, strings.Repeat("x", 8191), false},
		{"at limit", map[string]any{"fullOutput": strings.Repeat("x", 8192)}, strings.Repeat("x", 8192), true},
		{"above limit", map[string]any{"fullOutput": strings.Repeat("x", 8193)}, strings.Repeat("x", 8193), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			payload, err := json.Marshal(map[string]any{"type": "tool_progress_update", "toolUseId": "call", "update": tc.update})
			require.NoError(t, err)
			a.HandleOutput(droidAuditNotification(t, "main-session", payload))
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.Messages())
			assert.Equal(t, []agent.ProgressUpdate{agent.OutputTailProgress("droid-tool-call", tc.tail, tc.truncated)}, sink.ProgressUpdates())
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_result","toolUseId":"call","content":"result"}`)))
			assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("droid-tool-call"))
		})
	}
}

func TestToolProgressUpdateNeedsAToolIdentity(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_progress_update","update":{"text":"orphan"}}`)))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.ProgressUpdates())
}

func TestToolProgressUpdateStaysInTheChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"tool_progress_update","toolUseId":"child-call","update":{"fullOutput":"child output"}}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"tool_result","toolUseId":"child-call","content":"done"}`)))
	child := sink.Child(row.ChildAgentID)
	assert.Contains(t, child.ProgressUpdates(), agent.OutputTailProgress("droid-tool-child-call", "child output", false))
	assert.Contains(t, child.ProgressUpdates(), agent.CompleteOutputProgress("droid-tool-child-call"))
	assert.Empty(t, child.PersistedNotifications())
	assert.Empty(t, sink.ProgressUpdates())
}

func TestToolCallStoresOneRowWithTheFinalInput(t *testing.T) {
	t.Parallel()
	// .tmp/probe/droid/run2 frames 28 through 36 repeat one call as its input grows.
	for _, boundary := range []string{
		`{"type":"create_message","message":{"id":"assistant","role":"assistant","content":[{"type":"tool_use","id":"call_stream1"}]}}`,
		`{"type":"tool_execution_phase_changed","toolUseId":"call_stream1","phase":"queued"}`,
		`{"type":"tool_result","toolUseId":"call_stream1","content":"done"}`,
		`{"type":"agent_turn_completed","reason":"completed"}`,
		`{"type":"error","message":"failed"}`,
	} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			var last []byte
			for _, input := range []string{`{}`, `{"command":"echo streamed"}`, `{"command":"echo streamed","summary":"Print a marker"}`, `{"command":"echo streamed","summary":"Print a marker","riskLevel":"medium"}`} {
				last = []byte(`{"type":"tool_call","toolUse":{"type":"tool_use","id":"call_stream1","name":"Execute","input":` + input + `}}`)
				a.HandleOutput(droidAuditNotification(t, "", last))
			}
			assert.Empty(t, sink.Messages(), "streamed input is not a completed tool request")
			assert.Len(t, sink.OpenSpans(), 1)
			a.HandleOutput(droidAuditNotification(t, "", []byte(boundary)))
			var calls []agenttest.Message
			for _, row := range sink.Messages() {
				var header struct {
					Type string `json:"type"`
				}
				require.NoError(t, json.Unmarshal(row.Content, &header))
				if header.Type == "tool_call" {
					calls = append(calls, row)
				}
			}
			require.Len(t, calls, 1)
			assert.JSONEq(t, string(last), string(calls[0].Content))
		})
	}
}

func TestToolCallStoresOneRowBeforeThePermissionCard(t *testing.T) {
	t.Parallel()
	a, _ := newDroidControlTestAgent(t)
	sink := &droidPublicationSink{}
	var rowsAtPublication []agenttest.Message
	sink.publish = func() { rowsAtPublication = sink.Messages() }
	a.sink = agent.NewProviderServices(sink)
	call := []byte(`{"type":"tool_call","toolUse":{"id":"call","name":"Execute","input":{"command":"echo ready"}}}`)
	a.HandleOutput(droidAuditNotification(t, "", call))
	assert.Empty(t, sink.Messages())
	request := permissionRequest(t, "call", "Execute")
	a.onRequestPermission(&request)
	require.Len(t, rowsAtPublication, 1)
	assert.JSONEq(t, string(call), string(rowsAtPublication[0].Content))
	require.Equal(t, 1, sink.PublishedControlCount())
}

func TestToolCallWithoutAnIdentityStoresNoFakeSpan(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"tool_call","toolUse":{"name":"Execute","input":{}}}`)))
	assert.Empty(t, sink.OpenSpans())
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.Messages()[0].SpanID)
}

func TestToolCallStoresOneRowAfterAWriteFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	first := []byte(`{"type":"tool_call","toolUse":{"id":"call","name":"Execute","input":{"command":"first"}}}`)
	phase := []byte(`{"type":"tool_execution_phase_changed","toolUseId":"call","phase":"queued"}`)
	a.HandleOutput(droidAuditNotification(t, "", first))
	sink.PersistErr = assert.AnError
	a.HandleOutput(droidAuditNotification(t, "", phase))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"tool_call","toolUse":{"id":"call","name":"Execute","input":{"command":"later"}}}`)))
	attempts := len(sink.Messages())
	sink.PersistErr = nil
	a.HandleOutput(droidAuditNotification(t, "", phase))
	a.HandleOutput(droidAuditNotification(t, "", phase))
	rows := sink.Messages()[attempts:]
	require.Len(t, rows, 1)
	assert.JSONEq(t, string(first), string(rows[0].Content))
	assert.Len(t, sink.OpenSpans(), 1)
}

func TestToolCallStoresPendingRowsInTheirNativeOrderAtProcessEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, id := range []string{"z", "a"} {
		payload, err := json.Marshal(map[string]any{"type": "tool_call", "toolUse": map[string]any{"id": id, "name": "Read", "input": map[string]any{"file_path": id}}})
		require.NoError(t, err)
		a.HandleOutput(droidAuditNotification(t, "", payload))
	}
	assert.Empty(t, sink.Messages())
	a.finishOutput(agent.MessageCompletionInterrupted)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, "droid-tool-z", rows[0].SpanID)
	assert.Equal(t, "droid-tool-a", rows[1].SpanID)
}

func TestToolCallStoresOneRowInItsOwnChild(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	child, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	for _, session := range []string{"main-session", "child-1"} {
		payload, err := json.Marshal(map[string]any{"type": "tool_call", "toolUse": map[string]any{"id": "same", "name": "Read", "input": map[string]any{"file_path": session}}})
		require.NoError(t, err)
		a.HandleOutput(droidAuditNotification(t, session, payload))
	}
	phase := []byte(`{"type":"tool_execution_phase_changed","toolUseId":"same","phase":"queued"}`)
	a.HandleOutput(droidAuditNotification(t, "main-session", phase))
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.Child(child.ChildAgentID).Messages())
	a.HandleOutput(droidAuditNotification(t, "child-1", phase))
	require.Len(t, sink.Child(child.ChildAgentID).Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "main-session")
	assert.Contains(t, string(sink.Child(child.ChildAgentID).Messages()[0].Content), "child-1")
}

func TestLlmRetryResolvesAtTheFirstOutput(t *testing.T) {
	t.Parallel()
	// The retry payload comes from .tmp/probe/droid/run4b, frame 10.
	for _, output := range []string{
		`{"type":"assistant_text_delta","messageId":"answer","textDelta":"success"}`,
		`{"type":"thinking_text_delta","messageId":"thought","textDelta":"reasoning"}`,
		`{"type":"tool_call","toolUse":{"id":"call","name":"Read","input":{}}}`,
	} {
		t.Run(output, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"llm_retry","attempt":1,"reason":"overloaded"}`)))
			assert.Empty(t, sink.LeapMuxNotifications())
			a.HandleOutput(droidAuditNotification(t, "", []byte(output)))
			a.HandleOutput(droidAuditNotification(t, "", []byte(output)))
			assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
		})
	}
}

func TestLlmRetryEndsWithoutAResolutionOnFailureOrTurnEnd(t *testing.T) {
	t.Parallel()
	for _, end := range []string{`{"type":"error","message":"failed"}`, `{"type":"agent_turn_completed","reason":"cancelled"}`} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"llm_retry","attempt":1,"reason":"overloaded"}`)))
			a.HandleOutput(droidAuditNotification(t, "", []byte(end)))
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"next","textDelta":"new turn"}`)))
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestLlmRetryRetainsResolutionOrderAfterAWriteFailure(t *testing.T) {
	t.Parallel()
	a, raw, _ := newSteerAgent(t)
	sink := &droidRetryRecordingSink{Sink: raw}
	a.sink = agent.NewProviderServices(sink)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"llm_retry","attempt":1,"reason":"overloaded"}`)))
	raw.PersistErr = assert.AnError
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"answer","textDelta":"success"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_complete","messageId":"answer"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"answer","role":"assistant","content":[{"type":"text","text":"success"}]}}`)))
	assert.Empty(t, raw.Messages(), "the answer cannot pass a failed resolution write")
	assert.Equal(t, []string{"native"}, sink.events)
	raw.PersistErr = nil
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"droid_working_state_changed","newState":"streaming_assistant_message"}`)))
	assert.Equal(t, []string{"native", "worker", "message", "message"}, sink.events)
	require.Len(t, raw.PersistedNotifications(), 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw.PersistedNotifications()[0].Source)
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, raw.LeapMuxNotifications())
}

func TestLlmRetryIgnoresChildOutputAndEmptyDeltas(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"llm_retry","attempt":1,"reason":"overloaded"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"answer","textDelta":""}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"assistant_text_delta","messageId":"child-answer","textDelta":"child"}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	assert.Empty(t, sink.LeapMuxNotifications())
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"answer","textDelta":"parent"}`)))
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
}

func TestRetryAttemptDiscardsThePartial(t *testing.T) {
	t.Parallel()
	// The fixture trims .tmp/probe/droid/run4b frames 9 through 16 and uses the fixture's main-session identity.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "Droid stream retry", FramesPath: "testdata/llm_retry.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			t.Helper()
			a, _, _ := newSteerAgent(t)
			a.sink = agent.NewProviderServices(sink)
			return a.HandleOutput
		},
		Want:      []agenttest.RetryAttemptText{{Text: "FULL-AFTER-SERVER-ERROR", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"partial "},
	})
}

func TestRetractionDiscardsAnEarlierCompletedBlock(t *testing.T) {
	t.Parallel()
	// Droid's onContentBlockComplete emits these block ends. A later request attempt retracts the in-flight assistant message.
	for _, kind := range []string{"assistant", "thinking"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			for _, payload := range []string{
				`{"type":"` + kind + `_text_delta","messageId":"same","blockIndex":0,"textDelta":"FAILED_COMPLETE_BLOCK"}`,
				`{"type":"` + kind + `_text_complete","messageId":"same","blockIndex":0}`,
				`{"type":"llm_retry","attempt":1,"reason":"overloaded"}`,
				`{"type":"assistant_message_retracted","messageId":"same"}`,
				`{"type":"assistant_text_delta","messageId":"same","blockIndex":0,"textDelta":"FULL"}`,
				`{"type":"assistant_text_complete","messageId":"same","blockIndex":0}`,
				`{"type":"create_message","message":{"id":"same","role":"assistant","content":[{"type":"text","text":"FULL"}]}}`,
				`{"type":"agent_turn_completed","reason":"completed"}`,
			} {
				a.HandleOutput(droidAuditNotification(t, "", []byte(payload)))
			}
			for _, row := range sink.Messages() {
				assert.NotContains(t, string(row.Content), "FAILED_COMPLETE_BLOCK")
			}
		})
	}
}

func TestRetractionDropsAPendingToolCall(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"tool_call","toolUse":{"id":"failed-tool","name":"Read","input":{"file_path":"failed.txt"}}}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_message_retracted","messageId":"message"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	for _, row := range sink.Messages() {
		assert.NotContains(t, string(row.Content), "failed-tool")
	}
	assert.Contains(t, sink.ClosedSpans(), "droid-tool-failed-tool")
}

func TestUserOnlyNoticePersists(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	payload := []byte(`{"type":"create_message","message":{"id":"notice","role":"system","visibility":"user_only","content":[{"type":"text","text":"A native notice"}]}}`)
	a.HandleOutput(droidAuditNotification(t, "", payload))
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(payload), string(sink.Messages()[0].Content))
}

func TestUserOnlyNoticeAfterAnErrorIsDropped(t *testing.T) {
	t.Parallel()
	// .tmp/probe/droid/run1 frames 60 and 61 repeat the error in a user-only system notice.
	for _, session := range []string{"main-session", "child-1"} {
		t.Run(session, func(t *testing.T) {
			t.Parallel()
			a, root, _ := newSteerAgent(t)
			sink := root
			if session == "child-1" {
				a.HandleOutput([]byte(droidChildAvailable))
				child, ok := root.BackgroundTask("child-1")
				require.True(t, ok)
				sink = root.Child(child.ChildAgentID)
			}
			a.HandleOutput(droidAuditNotification(t, session, []byte(`{"type":"error","message":"400 PROBE-BAD-REQUEST"}`)))
			a.HandleOutput(droidAuditNotification(t, session, []byte(`{"type":"create_message","message":{"id":"duplicate","role":"system","visibility":"user_only","content":[{"type":"text","text":"BYOK Error: 400 PROBE-BAD-REQUEST"}]}}`)))
			assert.Empty(t, sink.Messages())
			require.Len(t, sink.PersistedNotifications(), 1)
			a.HandleOutput(droidAuditNotification(t, session, []byte(`{"type":"create_message","message":{"id":"other","role":"system","visibility":"user_only","content":[{"type":"text","text":"Another notice"}]}}`)))
			require.Len(t, sink.Messages(), 1)
			assert.Contains(t, string(sink.Messages()[0].Content), "Another notice")
		})
	}
}

func TestUserOnlyNoticeAfterTheTurnEndsDoesNotConsumeAnOldError(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"error","message":"failed"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"error"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"new","role":"system","visibility":"user_only","content":[{"type":"text","text":"New turn notice"}]}}`)))
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[1].Content), "New turn notice")
}

func TestThinkingDeltaCreatesAReasoningRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"Compare "}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"the values."}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1,"durationMs":8}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	assert.Empty(t, sink.Messages(), "the block end does not commit the message")
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"create_message","message":{"id":"message-1","role":"assistant","content":[{"type":"thinking","thinking":"Compare the values."}]}}`)))
	rows := sink.Messages()
	require.Len(t, rows, 2, "the native commit stores the thought and its metadata")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rows[0].Content, &payload))
	assert.Equal(t, contracts.AssembledMessageKindReasoning, payload[contracts.AssembledMessageFieldKind])
	assert.Equal(t, "Compare the values.", payload[contracts.AssembledMessageFieldText])
	assert.Empty(t, sink.PersistedNotifications(), "thinking frames do not become raw notifications")
}

func TestEmptyThinkingBlockCreatesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1}}}`))
	assert.Empty(t, sink.Messages(), "an empty thinking block has no content")
	assert.Empty(t, sink.PersistedNotifications(), "the completion is protocol state")
}

func TestTurnEndReportsZeroToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"assistant_text_delta","messageId":"text-1","blockIndex":0,"textDelta":"A text-only answer."}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"assistant_text_complete","messageId":"text-1","blockIndex":0}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"text-turn","reason":"completed"}}}`))

	assert.Equal(t, []int{0}, agenttest.TurnToolUseCounts(t, sink.Messages()))
}

func TestTurnEndCountsEachNativeToolResultOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	result := []byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_result","toolUseId":"read-1","content":"The read result.","isError":false}}}`)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"read-1","name":"Read","input":{"file_path":"note.txt"}}}}}`))
	a.HandleOutput(result)
	a.HandleOutput(result)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"tool-turn","reason":"completed"}}}`))

	assert.Equal(t, []int{1}, agenttest.TurnToolUseCounts(t, sink.Messages()), "a duplicate native result must not increase the count")
}

func TestToolResultPersistsBeforeItsSpanCloses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"shell-rail","name":"Execute","input":{"command":"printf result"}}}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_result","toolUseId":"shell-rail","content":"result","isError":false}}}`))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Closing)
	assert.Contains(t, rows[1].SpansOpenAtPersist, agenttest.SpanOpen{SpanID: "droid-tool-shell-rail"})
	assert.Equal(t, []string{"droid-tool-shell-rail"}, sink.ClosedSpans())
}

func TestTurnEndToolCountResetsAndExcludesChildCalls(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_call","toolUse":{"id":"reused-read","name":"Read","input":{"file_path":"child.txt"}}}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_result","toolUseId":"reused-read","content":"child result","isError":false}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"child-turn","reason":"completed"}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"reused-read","name":"Read","input":{"file_path":"root.txt"}}}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_result","toolUseId":"reused-read","content":"root result","isError":false}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"first-root-turn","reason":"completed"}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"second-root-turn","reason":"completed"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	assert.Equal(t, []int{1, 0}, agenttest.TurnToolUseCounts(t, sink.Messages()), "child activity cannot change the reset root count")
}

// These identities preserve the captured native call and session.
const droidAuditSessionID = "d5a2d492-459b-47f2-9b37-d0fe6f5abb74"
const droidAuditCallID = "call_native-full-artifact"

func droidAuditNotification(t *testing.T, sessionID string, payload []byte) []byte {
	t.Helper()
	env := newDroidEnvelope(droidTypeNotification)
	env.Method = droidMethodSessionNotif
	var err error
	env.Params, err = json.Marshal(droidNotification{SessionID: sessionID, Notification: payload})
	require.NoError(t, err)
	line, err := env.Marshal()
	require.NoError(t, err)
	return line
}

func TestDroidFullOutputPreservesActualProgressAndResultShapes(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	handshakeDone := make(chan error, 1)
	go func() {
		handshakeDone <- a.handshake(agent.Options{ResumeSessionID: droidAuditSessionID})
	}()
	select {
	case <-stdin.written:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the controlled native load request did not reach stdin")
	}
	frames := stdin.frames()
	require.Len(t, frames, 1)
	var nativeRequest droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &nativeRequest))
	require.Equal(t, droidMethodLoadSession, nativeRequest.Method)
	request := lastRequest(t, stdin)
	require.Equal(t, droidAuditSessionID, request["sessionId"])
	response := newDroidEnvelope(droidTypeResponse)
	response.ID = droidInitRequestID
	var err error
	response.Result, err = json.Marshal(map[string]any{
		"sessionId": droidAuditSessionID, "settings": map[string]any{}, "availableModels": []any{},
	})
	require.NoError(t, err)
	line, err := response.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	select {
	case err := <-handshakeDone:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the controlled native load response did not settle startup")
	}
	require.Equal(t, []string{droidAuditSessionID}, sink.SessionIDs())
	call := []byte(`{"type":"tool_call","toolUse":{"type":"tool_use","id":"call_native-full-artifact","name":"Execute","input":{"command":"printf ok","riskLevel":"low"}}}`)
	progress := []byte(`{"type":"tool_progress_update","toolUseId":"call_native-full-artifact","toolName":"Execute","update":{"type":"status","text":"","timestamp":1791035602380,"terminalId":"58abd506-6e96-404c-88b7-25152c70b972"}}`)
	result := []byte(`{"type":"tool_result","toolUseId":"call_native-full-artifact","messageId":"ab8f6d47-df92-4d88-b1fd-ac7cc6e6aec7","content":"native masked summary\n\nFull command output saved to: /unowned/droid-terminal-bxa7bv/58abd506-6e96-404c-88b7-25152c70b972.log (686KB)\n\n[Process exited with code 0]","isError":false}`)

	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, call))
	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, progress))
	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, result))

	assert.Empty(t, sink.PersistedNotifications(), "native tool progress stays live")
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("droid-tool-"+droidAuditCallID, "", false))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, droidAuditSessionID, row.AgentSessionID, "the native handshake binds every stored tool row to its session")
	}
	assert.Equal(t, result, rows[1].Content, "the native result keeps its masked summary, footer, and status")
	assert.Equal(t, "droid-tool-"+droidAuditCallID, rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	assert.Empty(t, rows[1].SupplementalContent, "an unowned printed path cannot create a receipt")
}

func TestDroidFullOutputForeignProgressCannotWriteTheRoot(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	progress := []byte(`{"type":"tool_progress_update","toolUseId":"call_native-full-artifact","toolName":"Execute","update":{"type":"status","text":"","timestamp":1791035602380,"terminalId":"58abd506-6e96-404c-88b7-25152c70b972"}}`)
	a.HandleOutput(droidAuditNotification(t, "c9ef87b5-4ce6-45a3-9753-7f1e44aad709", progress))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, a.outputStates)
}
