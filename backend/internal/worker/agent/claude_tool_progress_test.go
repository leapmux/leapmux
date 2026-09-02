package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures below are verbatim captures from `claude 2.1.258` running with
// the same flags StartClaudeCode passes (--output-format stream-json --verbose),
// so the field names and the synthetic tool_use_id shapes are the CLI's own and
// not a guess at them.
const (
	claudeHeartbeatFrame = `{"type":"tool_progress","tool_use_id":"toolu_01TZ7EZih11vksrqCqRgGVi2-heartbeat-0",` +
		`"tool_name":"Bash","parent_tool_use_id":"toolu_01TZ7EZih11vksrqCqRgGVi2","elapsed_time_seconds":30,` +
		`"heartbeat":true,"session_id":"995209bb-888f-46e8-8b21-f8c37c4f8043","uuid":"c984f7dd-41ff-4850-b2d4-519102d23649"}`

	claudeSubagentRetryFrame = `{"type":"tool_progress","tool_use_id":"agent_msg_01abc","tool_name":"Agent",` +
		`"parent_tool_use_id":"toolu_01LPaMFvQw8He49JbtUk7MZx","elapsed_time_seconds":0,"subagent_type":"Explore",` +
		`"subagent_retry":{"agent_id":"a-1","attempt":2,"max_retries":5,"retry_delay_ms":4000,` +
		`"error_status":529,"error_category":"overloaded"},"session_id":"s-1","uuid":"u-1"}`

	// The resolved signal: the SAME frame with subagent_retry absent. There is no
	// separate "resolved" type on the wire.
	claudeSubagentRetryResolvedFrame = `{"type":"tool_progress","tool_use_id":"agent_msg_01abc","tool_name":"Agent",` +
		`"parent_tool_use_id":"toolu_01LPaMFvQw8He49JbtUk7MZx","elapsed_time_seconds":0,"subagent_type":"Explore",` +
		`"session_id":"s-1","uuid":"u-2"}`
)

// runningToolUpdate returns the running_tool payload of the last broadcast, and
// fails the test when none was broadcast.
func runningToolUpdate(t *testing.T, sink *outputTestSink) map[string]interface{} {
	t.Helper()
	v, ok := lastSessionInfoValue(&sink.testSink, contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "expected a running_tool broadcast")
	update, ok := v.(map[string]interface{})
	require.True(t, ok, "running_tool must be an object, got %T", v)
	return update
}

func TestHandleOutput_ToolProgressHeartbeatBroadcastsTheRunningTool(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(claudeHeartbeatFrame))

	update := runningToolUpdate(t, sink)
	// Keyed on parent_tool_use_id. The frame's own tool_use_id is the synthetic
	// "<realId>-heartbeat-0", which matches no row in the transcript.
	assert.Equal(t, "toolu_01TZ7EZih11vksrqCqRgGVi2", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "Bash", update[contracts.RunningToolFieldToolName])
	assert.Equal(t, int64(30), update[contracts.RunningToolFieldElapsedSeconds])
	assert.NotContains(t, update, contracts.RunningToolFieldRetry,
		"a heartbeat says nothing about a retry, so it must not clear one")

	// Ephemeral: nothing reaches the timeline, and nothing reaches the streaming
	// text tail.
	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
	assert.Equal(t, 0, sink.StreamChunkCount())
}

// The leak this handler closes: before it existed, tool_progress fell to the
// default arm and was broadcast as a span-less stream chunk. With NO span open
// -- exactly the state during a top-level Agent/Task call, which opens none --
// the tracker let it through and the frontend appended the raw JSON to the
// chat's streaming text.
func TestHandleOutput_ToolProgressNeverReachesTheStreamingText(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	require.Empty(t, sink.OpenSpans(), "the leak needs no span open, which is the Agent-tool case")

	agent.HandleOutput([]byte(claudeHeartbeatFrame))
	agent.HandleOutput([]byte(claudeSubagentRetryFrame))

	assert.Equal(t, 0, sink.StreamChunkCount(),
		"a tool_progress frame must never be broadcast as a stream chunk")
}

func TestHandleOutput_ToolProgressHeartbeatsRaiseTheElapsedTime(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	for i, elapsed := range []int{30, 60, 90} {
		agent.HandleOutput([]byte(fmt.Sprintf(
			`{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-%d","tool_name":"Bash",`+
				`"parent_tool_use_id":"toolu_A","elapsed_time_seconds":%d,"heartbeat":true}`, i, elapsed)))
	}

	update := runningToolUpdate(t, sink)
	assert.Equal(t, int64(90), update[contracts.RunningToolFieldElapsedSeconds])

	sink.mu.Lock()
	broadcasts := len(sink.sessionInfos)
	sink.mu.Unlock()
	assert.Equal(t, 3, broadcasts, "every heartbeat ships; the badge steps at each one")
}

// Claude runs tools in parallel, so two heartbeats can interleave. Each must
// address its own card.
func TestHandleOutput_ToolProgressKeepsParallelToolsApart(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-0","tool_name":"Bash",` +
		`"parent_tool_use_id":"toolu_A","elapsed_time_seconds":30,"heartbeat":true}`))
	agent.HandleOutput([]byte(`{"type":"tool_progress","tool_use_id":"toolu_B-heartbeat-0","tool_name":"Read",` +
		`"parent_tool_use_id":"toolu_B","elapsed_time_seconds":30,"heartbeat":true}`))

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.sessionInfos, 2)
	first := sink.sessionInfos[0][contracts.SessionInfoKeyRunningTool].(map[string]interface{})
	second := sink.sessionInfos[1][contracts.SessionInfoKeyRunningTool].(map[string]interface{})
	assert.Equal(t, "toolu_A", first[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "Bash", first[contracts.RunningToolFieldToolName])
	assert.Equal(t, "toolu_B", second[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "Read", second[contracts.RunningToolFieldToolName])
}

func TestHandleOutput_ToolProgressSubagentRetryCarriesEveryField(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(claudeSubagentRetryFrame))

	update := runningToolUpdate(t, sink)
	assert.Equal(t, "toolu_01LPaMFvQw8He49JbtUk7MZx", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "Agent", update[contracts.RunningToolFieldToolName])
	assert.Equal(t, "Explore", update[contracts.RunningToolFieldSubagentType])
	// elapsed_time_seconds is always 0 on this family, so the key must be absent
	// rather than shipped as a 0 that would reset the heartbeat's clock.
	assert.NotContains(t, update, contracts.RunningToolFieldElapsedSeconds)

	retry, ok := update[contracts.RunningToolFieldRetry].(map[string]interface{})
	require.True(t, ok, "an unresolved retry ships an object")
	assert.Equal(t, 2, retry[contracts.RunningToolRetryFieldAttempt])
	assert.Equal(t, 5, retry[contracts.RunningToolRetryFieldMaxRetries])
	assert.Equal(t, int64(4000), retry[contracts.RunningToolRetryFieldRetryDelayMs])
	assert.Equal(t, 529, retry[contracts.RunningToolRetryFieldErrorStatus])
	assert.Equal(t, "overloaded", retry[contracts.RunningToolRetryFieldErrorCategory])
}

func TestHandleOutput_ToolProgressResolvedRetryShipsAnExplicitNull(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(claudeSubagentRetryResolvedFrame))

	update := runningToolUpdate(t, sink)
	// Present AND nil. An absent key would leave the badge stuck on the last
	// attempt; a nil one tells the frontend the retry resolved.
	require.Contains(t, update, contracts.RunningToolFieldRetry)
	assert.Nil(t, update[contracts.RunningToolFieldRetry])

	// It must survive the marshal the broadcast path performs, as JSON null.
	encoded, err := json.Marshal(update)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"retry":null`)
}

// error_status is nullable on the wire. A null must not become a 0, which reads
// as a real HTTP status.
func TestHandleOutput_ToolProgressRetryKeepsANullErrorStatusNull(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"tool_progress","tool_use_id":"agent_x","tool_name":"Agent",` +
		`"parent_tool_use_id":"toolu_A","elapsed_time_seconds":0,"subagent_type":"Plan",` +
		`"subagent_retry":{"agent_id":"a","attempt":1,"max_retries":3,"retry_delay_ms":1000,` +
		`"error_status":null,"error_category":"connection_error"}}`))

	retry := runningToolUpdate(t, sink)[contracts.RunningToolFieldRetry].(map[string]interface{})
	assert.Nil(t, retry[contracts.RunningToolRetryFieldErrorStatus])
	assert.Equal(t, "connection_error", retry[contracts.RunningToolRetryFieldErrorCategory])
}

func TestHandleOutput_ToolProgressDropsWhatNoCardCanCarry(t *testing.T) {
	t.Parallel()

	for name, line := range map[string]string{
		// Nothing to attach the badge to.
		"absent parent": `{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-0","tool_name":"Bash","elapsed_time_seconds":30,"heartbeat":true}`,
		"null parent":   `{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-0","tool_name":"Bash","parent_tool_use_id":null,"elapsed_time_seconds":30,"heartbeat":true}`,
		"empty parent":  `{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-0","tool_name":"Bash","parent_tool_use_id":"","elapsed_time_seconds":30,"heartbeat":true}`,
		// The bash_progress family: reachable only under CLAUDE_CODE_REMOTE or a
		// container id, so it is dropped rather than guessed at.
		"bash_progress": `{"type":"tool_progress","tool_use_id":"toolu_A","tool_name":"Bash","parent_tool_use_id":"toolu_A","elapsed_time_seconds":30,"task_id":"t-1"}`,
		// The repl_tool_call family: needs the REPL tool.
		"repl_call": `{"type":"tool_progress","tool_use_id":"toolu_A","tool_name":"REPL","parent_tool_use_id":"toolu_A","elapsed_time_seconds":0,"repl_call":{"phase":"start"}}`,
		"malformed": `{"type":"tool_progress",`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &outputTestSink{}
			agent := newTestAgent(sink)
			agent.HandleOutput([]byte(line))

			_, ok := lastSessionInfoValue(&sink.testSink, contracts.SessionInfoKeyRunningTool)
			assert.False(t, ok, "nothing to show, so nothing is broadcast")
			assert.Equal(t, 0, sink.StreamChunkCount())
			assert.Equal(t, 0, sink.MessageCount())
		})
	}
}

// A frame whose subagent_retry does not decode is dropped WHOLE rather than
// degraded to "running, no retry". That costs nothing: the retry family always
// reports elapsed_time_seconds 0, so a degraded update would carry no elapsed
// time and the badge would render nothing anyway.
func TestHandleOutput_ToolProgressDropsAnUndecodableRetry(t *testing.T) {
	t.Parallel()

	for name, retry := range map[string]string{
		"a string":         `"x"`,
		"a number":         `42`,
		"a mistyped field": `{"attempt":"2","max_retries":5}`,
		"an array":         `[1,2]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &outputTestSink{}
			agent := newTestAgent(sink)
			agent.HandleOutput([]byte(`{"type":"tool_progress","tool_use_id":"agent_x","tool_name":"Agent",` +
				`"parent_tool_use_id":"toolu_A","elapsed_time_seconds":0,"subagent_type":"Plan",` +
				`"subagent_retry":` + retry + `}`))

			_, ok := lastSessionInfoValue(&sink.testSink, contracts.SessionInfoKeyRunningTool)
			assert.False(t, ok)
			assert.Equal(t, 0, sink.StreamChunkCount(), "and it still never reaches the chat tail")
		})
	}
}

// tool_name is not optional on the wire, but an absent one must not stop the
// badge reporting the elapsed time: the frontend seeds an empty name rather
// than rendering "undefined".
func TestHandleOutput_ToolProgressHeartbeatWithoutAToolName(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"tool_progress","tool_use_id":"toolu_A-heartbeat-0",` +
		`"parent_tool_use_id":"toolu_A","elapsed_time_seconds":30,"heartbeat":true}`))

	update := runningToolUpdate(t, sink)
	assert.Equal(t, "toolu_A", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "", update[contracts.RunningToolFieldToolName])
	assert.Equal(t, int64(30), update[contracts.RunningToolFieldElapsedSeconds])
}

func TestClaudeNonNegativeCount(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		raw  string
		want int64
	}{
		"plain integer":      {`30`, 30},
		"absent":             {``, 0},
		"quoted":             {`"30"`, 0},
		"fractional":         {`230.0`, 230},
		"exponent":           {`1.5e4`, 15000},
		"truncates down":     {`29.9`, 29},
		"negative clamps":    {`-5`, 0},
		"negative fraction":  {`-0.5`, 0},
		"float64 overflow":   {`1e400`, 0},
		"int64 out of range": {`1e300`, 0},
		"not a number":       {`{"a":1}`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, claudeNonNegativeCount(json.RawMessage(tc.raw)))
		})
	}
}

// The shared clamp must give the thinking-token estimate the same answers it
// gave before it was extracted.
func TestParseThinkingTokens_SanitizesThroughTheSharedClamp(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		line string
		want int64
		ok   bool
	}{
		"plain":             {`{"type":"system","subtype":"thinking_tokens","estimated_tokens":230}`, 230, true},
		"negative":          {`{"type":"system","subtype":"thinking_tokens","estimated_tokens":-5}`, 0, true},
		"overflow":          {`{"type":"system","subtype":"thinking_tokens","estimated_tokens":1e400}`, 0, true},
		"out of range":      {`{"type":"system","subtype":"thinking_tokens","estimated_tokens":1e300}`, 0, true},
		"quoted":            {`{"type":"system","subtype":"thinking_tokens","estimated_tokens":"230"}`, 0, true},
		"absent count":      {`{"type":"system","subtype":"thinking_tokens"}`, 0, true},
		"another subtype":   {`{"type":"system","subtype":"init","estimated_tokens":230}`, 0, false},
		"not a system line": {`{"type":"assistant"}`, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseThinkingTokens([]byte(tc.line))
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The default arm forwards stream_event and NOTHING else. It used to forward
// every unknown type as a span-less stream chunk, which printed the raw line
// into the chat tail.
func TestHandleOutput_OnlyStreamEventReachesTheStreamingText(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{"type":"stream_event","event":{"type":"content_block_delta"}}`))
	require.Equal(t, 1, sink.StreamChunkCount(), "stream_event still streams")
	assert.Empty(t, sink.LastStreamChunk().SpanID)

	for _, line := range []string{
		`{"type":"tool_use_summary","summary":"x"}`,
		`{"type":"sdk_status","status":"requesting"}`,
		`{"type":"compact_progress"}`,
		`{"type":"an_unknown_future_frame"}`,
	} {
		agent.HandleOutput([]byte(line))
	}
	assert.Equal(t, 1, sink.StreamChunkCount(), "an unknown type is dropped, not printed")
	assert.Equal(t, 0, sink.MessageCount())
}
