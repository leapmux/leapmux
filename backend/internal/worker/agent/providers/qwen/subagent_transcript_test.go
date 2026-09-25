package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// backgroundRecords is the transcript of one background subagent, as Qwen
// 0.24.3 wrote it (probe `subagents/<session>/agent-general-purpose-call_*.jsonl`),
// with the workspace path shortened and the bookkeeping fields dropped. The
// fifth record is a message that the parent sent to the running child.
var backgroundRecords = []string{
	`{"uuid":"d0d8","type":"user","isSidechain":true,"message":{"role":"user","parts":[{"text":"CHILD_TASK: list the files"}]}}`,
	`{"uuid":"a6df","type":"assistant","isSidechain":true,"message":{"role":"model","parts":[{"text":"Child thinking.","thought":true},{"text":"Child will list."}]},"usageMetadata":{"promptTokenCount":100}}`,
	`{"uuid":"021a","type":"assistant","isSidechain":true,"message":{"role":"model","parts":[{"functionCall":{"id":"call_22ab86821a","name":"list_directory","args":{"path":"/ws"}}}]}}`,
	`{"uuid":"53d4","type":"tool_result","isSidechain":true,"message":{"role":"user","parts":[{"functionResponse":{"id":"call_22ab86821a","name":"list_directory","response":{"output":"Listed 1 item(s) in /ws:\n---\ntouched.txt"}}}]},"toolCallResult":{"callId":"call_22ab86821a","durationMs":2}}`,
	`{"uuid":"6a01","type":"user","isSidechain":true,"message":{"role":"user","parts":[{"text":"send_message: also check the tests"}]}}`,
	`{"uuid":"e111","type":"assistant","isSidechain":true,"message":{"role":"model","parts":[{"text":"Child done: listed."}]}}`,
}

// convertAll converts records and decodes each update.
func convertAll(t *testing.T, records ...string) ([]map[string]any, []string) {
	t.Helper()
	var converter transcriptConverter
	var updates []map[string]any
	var userTexts []string
	for _, line := range records {
		var record chatRecord
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		converted := converter.convert(record)
		userTexts = append(userTexts, converted.userTexts...)
		for _, raw := range converted.updates {
			var update map[string]any
			require.NoError(t, json.Unmarshal(raw, &update))
			updates = append(updates, update)
		}
	}
	return updates, userTexts
}

func TestTranscriptConverterFollowsQwensLiveStream(t *testing.T) {
	t.Parallel()
	updates, userTexts := convertAll(t, backgroundRecords...)

	require.Len(t, updates, 5)
	assert.Equal(t, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Child thinking."}}, updates[0])
	assert.Equal(t, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Child will list."}}, updates[1])
	assert.Equal(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_22ab86821a", "status": "pending", "title": "list_directory",
		"kind": "other", "content": []any{}, "rawInput": map[string]any{"path": "/ws"},
		"_meta": map[string]any{"toolName": "list_directory"},
	}, updates[2])
	assert.Equal(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_22ab86821a", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Listed 1 item(s) in /ws:\n---\ntouched.txt"}}},
		"_meta":   map[string]any{"toolName": "list_directory"},
	}, updates[3])
	assert.Equal(t, "Child done: listed.", updates[4]["content"].(map[string]any)["text"])
	assert.Equal(t, []string{"send_message: also check the tests"}, userTexts, "the first user record is the spawn's prompt")
}

func TestTranscriptConverterReadsAFailedTool(t *testing.T) {
	t.Parallel()
	updates, _ := convertAll(t,
		`{"type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"c1","name":"run_shell_command","args":{"command":"false"}}}]}}`,
		`{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"run_shell_command","response":{"error":"exit 1"}}}]},"toolCallResult":{"callId":"c1","status":"error","error":{"message":"Command failed"}}}`,
	)
	require.Len(t, updates, 2)
	assert.Equal(t, "failed", updates[1]["status"])
	assert.Equal(t, []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Command failed"}}}, updates[1]["content"],
		"the error of the result wins over the response text, as in Qwen's replay")
}

func TestTranscriptConverterReadsTheResponseError(t *testing.T) {
	t.Parallel()
	// Qwen's own record of a call to a tool that does not exist, because the
	// model gave a wrong tool name: the error rides in the response, and the
	// result states no error of its own.
	updates, _ := convertAll(t,
		`{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c2","name":"list_directory","response":{"error":"Tool \"list_directory\" not found."}}}]},"toolCallResult":{"callId":"c2","durationMs":0}}`,
	)
	require.Len(t, updates, 1)
	assert.Equal(t, "completed", updates[0]["status"])
	assert.Equal(t, `Tool "list_directory" not found.`, updates[0]["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"])
}

func TestTranscriptConverterReadsAnEditAsADiff(t *testing.T) {
	t.Parallel()
	updates, _ := convertAll(t,
		`{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c3","name":"edit","response":{"output":"ok"}}}]},"toolCallResult":{"callId":"c3","status":"success","resultDisplay":{"fileName":"a.go","filePath":"/ws/a.go","originalContent":"old","newContent":"new","fileDiff":"@@"}}}`,
	)
	require.Len(t, updates, 1)
	assert.Equal(t, []any{map[string]any{"type": "diff", "path": "/ws/a.go", "oldText": "old", "newText": "new"}}, updates[0]["content"])
	assert.Equal(t, "a.go", updates[0]["rawOutput"].(map[string]any)["fileName"])
}

func TestTranscriptConverterSkipsWhatTheLiveStreamNeverShows(t *testing.T) {
	t.Parallel()
	updates, userTexts := convertAll(t,
		`{"type":"system","subtype":"ui_telemetry","systemPayload":{}}`,
		`{"type":"user","subtype":"slash_command","message":{"role":"user","parts":[{"text":"/compress"}]}}`,
		`{"type":"user","message":{"role":"user","parts":[{"text":"   "}]}}`,
		`{"type":"assistant","message":{"role":"model","parts":[{"text":""},{"functionCall":{"name":"no_id"}}]}}`,
		`{"type":"tool_result","message":{"role":"user","parts":[]}}`,
		`{"type":"unknown"}`,
	)
	assert.Empty(t, updates)
	assert.Empty(t, userTexts)
}

func TestBackgroundTranscriptPath(t *testing.T) {
	t.Parallel()
	for path, ok := range map[string]bool{
		"/home/u/.qwen/projects/-ws/subagents/s1/agent-general-purpose-call_1.jsonl": true,
		"/home/u/.qwen/projects/-ws/chats/s1.jsonl":                                  false,
		"relative/subagents/s1/agent-x.jsonl":                                        false,
		"/home/u/.qwen/projects/-ws/subagents/s1/agent-x.json":                       false,
		"/etc/passwd.jsonl":                        false,
		"/x/subagents/s1/../../../etc/agent.jsonl": false,
	} {
		_, got := backgroundTranscriptPath(path)
		assert.Equal(t, ok, got, path)
	}
}

func TestTranscriptTailKeepsAPartialLineForTheNextRead(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	tail := newTranscriptTail(a, "row", "/unused")
	record := backgroundRecords[2]

	tail.consume([]byte(record[:20]))
	assert.Equal(t, record[:20], string(tail.partial), "half a record waits for its end")
	assert.Empty(t, tail.convert.callNames, "half a record reaches no converter")

	tail.consume([]byte(record[20:] + "\n"))
	assert.Empty(t, tail.partial)
	assert.Equal(t, map[string]string{"call_22ab86821a": "list_directory"}, tail.convert.callNames, "the joined record reached the converter")
}

func TestTranscriptTailSkipsALineThatPassesTheLimit(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	tail := newTranscriptTail(a, "row", "/unused")
	tail.consume([]byte(strings.Repeat("x", transcriptLineLimit+1)))
	assert.True(t, tail.skipping)
	assert.Empty(t, tail.partial, "an oversize line is not held in memory")
	tail.consume([]byte("tail of the long line\n" + backgroundRecords[0] + "\n"))
	assert.False(t, tail.skipping, "the next line is read again")
	assert.True(t, tail.convert.promptSeen, "the record after the long line reached the converter")
}

func TestTranscriptTailOfAMissingFileReadsNothing(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	tail := newTranscriptTail(a, "row", "/nonexistent/subagents/s/agent-x.jsonl")
	tail.poll()
	assert.Zero(t, tail.offset)
	tail.stop(true)
}

// The next poll reads a transcript whole when it grew by more than one read
// between two polls. The poll reads over several reads, and it joins each
// record that straddles a read boundary.
func TestTranscriptTailReadsAGrowthLargerThanOneRead(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	path := filepath.Join(t.TempDir(), "agent-x.jsonl")
	pad := strings.Repeat("x", 1000)
	var records []string
	for i := 0; len(records)*len(pad) < 2*transcriptReadLimit; i++ {
		records = append(records, fmt.Sprintf(`{"type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"call_%05d","name":"read_file","args":{"pad":%q}}}]}}`, i, pad))
	}
	appendRecords(t, path, records...)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(transcriptReadLimit), "the growth passes one read")
	tail := newTranscriptTail(a, "row", path)

	tail.poll()

	assert.Len(t, tail.convert.callNames, len(records), "every record reached the converter")
	assert.Equal(t, info.Size(), tail.offset)
	assert.Empty(t, tail.partial)
}

// The reader skips a record that it cannot read, and the records after it
// still reach the converter.
func TestTranscriptTailSkipsAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	tail := newTranscriptTail(a, "row", "/unused")

	tail.consume([]byte("{not json\n\n   \n" + backgroundRecords[2] + "\n"))

	assert.Equal(t, map[string]string{"call_22ab86821a": "list_directory"}, tail.convert.callNames)
}

// convertOne converts one record with a converter that saw calls first, and
// decodes its single update.
func convertOne(t *testing.T, converter *transcriptConverter, line string) map[string]any {
	t.Helper()
	var record chatRecord
	require.NoError(t, json.Unmarshal([]byte(line), &record))
	converted := converter.convert(record)
	require.Len(t, converted.updates, 1, line)
	var update map[string]any
	require.NoError(t, json.Unmarshal(converted.updates[0], &update))
	return update
}

func TestTranscriptConverterToolResultFallbacks(t *testing.T) {
	t.Parallel()
	text := func(value string) []any {
		return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": value}}}
	}
	for _, tc := range []struct {
		name       string
		record     string
		status     string
		content    []any
		rawOutput  any
		toolName   string
		toolCallID string
	}{
		{
			name:   "the response states the call",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c9","name":"grep","response":{"output":"hit"}}}]}}`,
			status: "completed", content: text("hit"), toolName: "grep", toolCallID: "c9",
		},
		{
			name:   "a cancelled call",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"grep","response":{"output":"partial"}}}]},"toolCallResult":{"callId":"c1","status":"cancelled"}}`,
			status: "failed", content: text("partial"), toolName: "grep", toolCallID: "c1",
		},
		{
			name:   "a truncated edit",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c2","name":"edit","response":{"output":"ok"}}}]},"toolCallResult":{"callId":"c2","resultDisplay":{"fileName":"a.go","newContent":"x","truncatedForSession":true}}}`,
			status: "completed", content: text("ok"), toolName: "edit", toolCallID: "c2",
			rawOutput: map[string]any{"fileName": "a.go", "newContent": "x", "truncatedForSession": true},
		},
		{
			name:   "an edit with a file name alone",
			record: `{"type":"tool_result","toolCallResult":{"callId":"c3","resultDisplay":{"fileName":"a.go","originalContent":"","newContent":"new"}}}`,
			status: "completed", content: []any{map[string]any{"type": "diff", "path": "a.go", "oldText": "", "newText": "new"}}, toolName: "", toolCallID: "c3",
			rawOutput: map[string]any{"fileName": "a.go", "originalContent": "", "newContent": "new"},
		},
		{
			name:   "a response with other fields",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c4","name":"glob","response":{"matches":3}}}]},"toolCallResult":{"callId":"c4","resultDisplay":null}}`,
			status: "completed", content: text(`{"matches":3}`), toolName: "glob", toolCallID: "c4",
		},
		{
			name:   "an empty response",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c5","name":"noop","response":{}}}]}}`,
			status: "completed", content: []any{}, toolName: "noop", toolCallID: "c5",
		},
		{
			name:   "an error object with no message",
			record: `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c6","name":"grep","response":{"output":"text"}}}]},"toolCallResult":{"callId":"c6","status":"error","error":{"code":7}}}`,
			status: "failed", content: text("text"), toolName: "grep", toolCallID: "c6",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var converter transcriptConverter
			update := convertOne(t, &converter, tc.record)
			assert.Equal(t, "tool_call_update", update["sessionUpdate"])
			assert.Equal(t, tc.toolCallID, update["toolCallId"])
			assert.Equal(t, tc.status, update["status"])
			assert.Equal(t, tc.content, update["content"])
			assert.Equal(t, map[string]any{contracts.QwenMetaToolName: tc.toolName}, update["_meta"])
			if tc.rawOutput == nil {
				assert.NotContains(t, update, "rawOutput")
			} else {
				assert.Equal(t, tc.rawOutput, update["rawOutput"])
			}
		})
	}
}

// The name of the call wins over the name that its response states, and the
// converter forgets a call once its result went by.
func TestTranscriptConverterForgetsACallAtItsResult(t *testing.T) {
	t.Parallel()
	var converter transcriptConverter
	call := `{"type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"c1","name":"run_shell_command"}}]}}`
	result := `{"type":"tool_result","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"shell","response":{"output":"ok"}}}]},"toolCallResult":{"callId":"c1"}}`

	opened := convertOne(t, &converter, call)
	assert.Equal(t, map[string]any{}, opened["rawInput"], "a call with no arguments states an empty object")
	assert.Equal(t, map[string]any{contracts.QwenMetaToolName: "run_shell_command"}, convertOne(t, &converter, result)["_meta"])
	assert.Empty(t, converter.callNames)
	assert.Equal(t, map[string]any{contracts.QwenMetaToolName: "shell"}, convertOne(t, &converter, result)["_meta"],
		"a repeated result falls back to the name that the response states")
}
