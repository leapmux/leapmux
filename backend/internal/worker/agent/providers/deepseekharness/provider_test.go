package deepseekharness

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderPreservesRawControlResponses(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, Registration().Plugin)
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, Registration().Plugin)
}

// A stored request that does not decode, or that asks nothing, gives LeapMux
// no request to answer. One whose eventId is not the key that stores it gives
// another key, so the Worker refuses it before the provider reads it
// (agent.StoredControlRequestAgrees).
func TestProviderWithholdsAValidNativeAnswerForAnInvalidStoredRequest(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","answers":[{"id":"native-question","selected":["Second"]}]}}}`)
	question := func(eventID string) string {
		return `{"type":"waterfall","event":"user-questions/request","eventId":"` + eventID + `","agentId":"native-root","request":{"questions":[{"id":"native-question","question":"Which?","options":[{"label":"First"},{"label":"Second"}]}]}}`
	}
	for _, request := range []string{
		"not json",
		"null",
		`{"type":"waterfall","event":"user-questions/request","eventId":"native-event","agentId":"native-root","request":{"questions":null}}`,
		`{"type":"waterfall","event":"user-questions/request","eventId":"native-event","request":{"questions":[{"id":"native-question","question":"Which?","options":[{"label":"First"}]}]}}`,
	} {
		resolution := agenttest.ResolveAnswer(Registration().Plugin, []byte(request), "native-event", agent.ControlAnswer{Content: content})
		assert.Equal(t, content, resolution.Content)
		assert.True(t, resolution.Withhold, request)
		assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableRequest, request)
	}
	resolution := agenttest.ResolveAnswer(Registration().Plugin, []byte(question("native-event")), "native-event", agent.ControlAnswer{Content: content})
	assert.False(t, resolution.Withhold, "the request under its own key takes the answer")
}

func TestProviderRefusesUnrecognizedControlBehaviorAndMixedPlanReview(t *testing.T) {
	t.Parallel()
	plugin := Registration().Plugin
	approval := []byte(`{"type":"waterfall","event":"approval/request","eventId":"ev-1","agentId":"native-root","request":{"toolName":"bash","callId":"call-1"}}`)
	for _, content := range []string{
		`{"type":"control_response","response":{"subtype":"success","request_id":"ev-1","response":{"behavior":""}}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"ev-1","response":{"behavior":"maybe"}}}`,
	} {
		resolution := agenttest.ResolveAnswer(plugin, approval, "ev-1", agent.ControlAnswer{Content: []byte(content)})
		assert.True(t, resolution.Withhold, content)
		assert.EqualError(t, resolution.Refusal(), agent.RefusalNoDecision, content)
	}

	mixed := []byte(`{"type":"waterfall","event":"user-questions/request","eventId":"ev-mix","agentId":"native-root","request":{"questions":[{"id":"p","question":"Approve?","options":[{"label":"Approve"}],"intent":{"kind":"plan-review","approve":"Approve","callId":"call-p"}},{"id":"q","question":"Other?","options":[{"label":"Yes"}]}]}}`)
	allow := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ev-mix","response":{"behavior":"allow"}}}`)
	mixedRes := agenttest.ResolveAnswer(plugin, mixed, "ev-mix", agent.ControlAnswer{Content: allow})
	assert.True(t, mixedRes.Withhold)
	assert.EqualError(t, mixedRes.Refusal(), agent.RefusalUnreadableRequest)

	planReview := []byte(`{"type":"waterfall","event":"user-questions/request","eventId":"ev-plan","agentId":"native-root","request":{"questions":[{"id":"p","question":"Approve?","detail":"# Plan","options":[{"label":"Approve"},{"label":"Revise"}],"intent":{"kind":"plan-review","approve":"Approve","callId":"call-p"}}]}}`)
	planRes := plugin.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "ev-plan",
		RequestPayload:  planReview,
		ResponseContent: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ev-plan","response":{"behavior":"allow"}}}`),
	})
	assert.False(t, planRes.Withhold)
	assert.Equal(t, agent.PlanModeControlExit, planRes.PlanModeControl, "valid plan-review payload must classify as PlanModeControlExit even when ToolName is empty")

	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"wrong type", `{"type":"emit","event":"approval/request","eventId":"ev-s","agentId":"native-root","request":{"toolName":"bash"}}`},
		{"approval without toolName", `{"type":"waterfall","event":"approval/request","eventId":"ev-s","agentId":"native-root","request":{"callId":"c1"}}`},
		{"top-level questions without request object", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","questions":[{"id":"q1","question":"Pick?"}]}`},
		{"empty questions array", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[]}}`},
		{"non-object question element", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":["bad",{"id":"q1","question":"Pick?"}]}}`},
		{"duplicate question id", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"q1","question":"One?"},{"id":"q1","question":"Two?"}]}}`},
		{"non-array options", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"q1","question":"Pick?","options":{}}]}}`},
		{"empty option label", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"q1","question":"Pick?","options":[{"label":""}]}]}}`},
		{"duplicate option label", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"q1","question":"Pick?","options":[{"label":"A"},{"label":"A"}]}]}}`},
		{"plan review without callId", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"p","question":"Approve?","options":[{"label":"Approve"}],"intent":{"kind":"plan-review","approve":"Approve"}}]}}`},
		{"plan review with unmatched approve label", `{"type":"waterfall","event":"user-questions/request","eventId":"ev-s","agentId":"native-root","request":{"questions":[{"id":"p","question":"Approve?","options":[{"label":"Keep"}],"intent":{"kind":"plan-review","approve":"Approve","callId":"call-p"}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := agenttest.ResolveAnswer(plugin, []byte(tc.payload), "ev-s", agent.ControlAnswer{
				Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ev-s","response":{"behavior":"allow"}}}`),
			})
			assert.True(t, res.Withhold)
			assert.EqualError(t, res.Refusal(), agent.RefusalUnreadableRequest)
		})
	}

	emptyPayloadExit := plugin.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "ev-no-payload",
		ToolName:        contracts.DeepseekHarnessToolExitPlanMode,
		ResponseContent: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ev-no-payload","response":{"behavior":"allow"}}}`),
	})
	assert.False(t, emptyPayloadExit.Withhold)
	assert.Equal(t, agent.PlanModeControlExit, emptyPayloadExit.PlanModeControl)

	emptyPayloadBash := plugin.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "ev-no-payload-2",
		ToolName:        "bash",
		ResponseContent: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ev-no-payload-2","response":{"behavior":"allow"}}}`),
	})
	assert.False(t, emptyPayloadBash.Withhold)
	assert.Equal(t, agent.PlanModeControlNone, emptyPayloadBash.PlanModeControl)
}

// The key of a stored control is its eventId, which the agent publishes it
// under, so the Worker refuses a row whose payload states another eventId.
func TestControlRequestKeyIsTheEventID(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	publishPermission(t, a, "approval")
	require.Equal(t, 1, sink.PublishedControlCount())
	agenttest.AssertPublishedControlKeys(t, Registration().Plugin, sink)
	for _, payload := range []string{
		`{"type":"waterfall","event":"approval/request","agentId":"native-root"}`,
		`{"type":"waterfall","event":"approval/request","eventId":"","agentId":"native-root"}`,
		`{"type":"waterfall","event":"approval/request","eventId":7,"agentId":"native-root"}`,
	} {
		key, ok := Registration().Plugin.ControlRequestKey([]byte(payload), "")
		assert.False(t, ok, payload)
		assert.Empty(t, key, payload)
	}
}

func TestProviderPreservesNativeResumeTokens(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

func TestProviderStatesTheContinuableChildMethodSet(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil), optionmap.Map{contracts.DeepseekHarnessOptionChildMode: "continuable"})
	for _, mode := range []string{"", "one-shot", "unknown"} {
		assert.Equal(t, agent.ChildCapabilities{}, Registration().Plugin.ChildCapabilities(optionmap.Map{contracts.DeepseekHarnessOptionChildMode: mode}))
	}
}

func TestProviderPreservesOriginalBytesAndBlockIndex(t *testing.T) {
	raw := []byte(`{ "type":"assistant/message", "data":{"message":{"content":[{"type":"reasoning","text":"Think"},{"type":"text","text":"Answer"}]}} }`)
	content := agent.MessageContent{Original: raw, Supplemental: []byte(`{"blockIndex":0}`)}
	resolved := deepseekHarnessProvider{}.ResolveProviderData(content)
	var value map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolved, &value))
	assert.JSONEq(t, `0`, string(value["blockIndex"]))
	assert.Equal(t, raw, content.Original)
	for _, extra := range []string{`{"blockIndex":null}`, `{"blockIndex":-1}`, `{"blockIndex":"0"}`, `invalid`} {
		resolved = deepseekHarnessProvider{}.ResolveProviderData(agent.MessageContent{Original: raw, Supplemental: []byte(extra)})
		var record map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(resolved, &record))
		assert.NotContains(t, record, "blockIndex")
	}
}

func TestProviderCountsOnlyValidatedTurnEnds(t *testing.T) {
	provider := deepseekHarnessProvider{}
	for _, count := range []string{"0", "2"} {
		value, ok := provider.TurnEndToolUses([]byte(`{"type":"turn/end","` + contracts.MessageMetadataFieldToolUses + `":` + count + `}`))
		require.True(t, ok)
		assert.Equal(t, map[string]int32{"0": 0, "2": 2}[count], value)
	}
	for _, raw := range []string{`{}`, `{"type":"other","num_tool_uses":2}`, `{"type":"turn/end"}`, `{"type":"turn/end","num_tool_uses":-1}`, `{"type":"turn/end","num_tool_uses":2147483648}`, `{"type":"turn/end","num_tool_uses":"2"}`, `{"type":"turn/end","tool_uses":2}`} {
		_, ok := provider.TurnEndToolUses([]byte(raw))
		assert.False(t, ok)
	}
}

func TestClassifyEveryCompactionEndIsABoundary(t *testing.T) {
	provider := deepseekHarnessProvider{}
	assert.Equal(t, agent.NotificationKindCompactionBoundary, provider.Classify(json.RawMessage(`{"type":"compaction/end","data":{}}`)).Kind())
	assert.Equal(t, agent.NotificationKindCompactionBoundary, provider.Classify(json.RawMessage(`{"type":"compaction/end","data":{"error":"failed"}}`)).Kind())
	assert.Empty(t, provider.Classify(json.RawMessage(`{"type":"compaction/start","data":{}}`)).Kind())
	assert.Empty(t, provider.Classify(json.RawMessage(`invalid`)).Kind())
}

func TestProviderReadsNativeTodoSnapshots(t *testing.T) {
	provider := deepseekHarnessProvider{}
	event, ok := provider.ExtractTodoEvent("", []byte(`{"type":"todo/write","data":{"todos":[{"content":"Read","status":"completed"},{"content":"Change","status":"in_progress"}]}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 2)
	assert.Equal(t, "1", event.Snapshot[0].ID)
	assert.Equal(t, "Read", event.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[0].Status)
	assert.Equal(t, todoevents.StatusInProgress, event.Snapshot[1].Status)
	event, ok = provider.ExtractTodoEvent("", []byte(`{"type":"todo/write","data":{"todos":[]}}`), nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
	for _, raw := range []string{`{"type":"todo/write","data":{}}`, `{"type":"todo/write","data":{"todos":null}}`, `{"type":"todo/write","data":{"todos":[{"content":"x","status":"wrong"}]}}`, `{"type":"other","data":{"todos":[]}}`} {
		_, ok := provider.ExtractTodoEvent("", []byte(raw), nil)
		assert.False(t, ok)
	}
}

func TestProviderReadsTheWorkerCountBesideTheExactNativeTurnEnd(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	native := []byte(`{"type":"turn/end","seq":23,"time":1790955904251,"data":{"turn":1,"reason":{"kind":"completed"}}}`)
	for _, count := range []int{0, 2, 2147483647} {
		content := agent.WithToolUseCount(agent.MessageContent{Original: native}, count)
		resolved := agent.ResolveMessageContent(provider, content)
		actual, present := provider.TurnEndToolUses(resolved)
		require.True(t, present, "the provider must read the authoritative Worker count, including zero")
		assert.Equal(t, int32(count), actual)
		assert.Equal(t, native, content.Original, "the Worker metadata must not rewrite native bytes")
		assert.Contains(t, string(resolved), contracts.MessageMetadataFieldToolUses)
	}
}

func TestProviderRefusesAbsentOrInvalidWorkerCountsForANativeTurnEnd(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	native := []byte(`{"type":"turn/end","seq":23,"time":1790955904251,"data":{"turn":1,"reason":{"kind":"completed"}}}`)
	for _, value := range []string{"", "null", "-1", "2147483648", "1.5", `"2"`, "false", "[]", "{}"} {
		var metadata []byte
		if value != "" {
			metadata = []byte(`{"` + contracts.MessageMetadataFieldToolUses + `":` + value + `}`)
		}
		content := agent.MessageContent{Original: native, Metadata: metadata}
		resolved := agent.ResolveMessageContent(provider, content)
		_, present := provider.TurnEndToolUses(resolved)
		assert.False(t, present, "an absent or invalid Worker count cannot identify tool activity")
		assert.Equal(t, native, content.Original)
	}
	content := agent.WithToolUseCount(agent.MessageContent{Original: []byte(`{"type":"assistant/message","seq":23,"time":1790955904251,"data":{}}`)}, 2)
	_, present := provider.TurnEndToolUses(agent.ResolveMessageContent(provider, content))
	assert.False(t, present, "the metadata count belongs only to a native turn end")
}
