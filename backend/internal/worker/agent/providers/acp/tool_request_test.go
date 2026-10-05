package acp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acpNotificationLine is one session/update line that sessionID sends.
func acpNotificationLine(t *testing.T, sessionID, update string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": acpMethodSessionUpdate, "params": sessionUpdate(t, sessionID, update)})
	require.NoError(t, err)
	return frame
}

// acpPermissionLine is one session/request_permission that sessionID sends for
// toolCall, with two of the options that Fast Agent offers. An empty sessionID
// leaves the field out.
func acpPermissionLine(t *testing.T, id int, sessionID, toolCall string) []byte {
	t.Helper()
	params := map[string]any{
		"toolCall": json.RawMessage(toolCall),
		"options": []map[string]string{
			{"optionId": "allow_once", "name": "Allow Once", "kind": "allow_once"},
			{"optionId": "reject_once", "name": "Reject Once", "kind": "reject_once"},
		},
	}
	if sessionID != "" {
		params["sessionId"] = sessionID
	}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": acpMethodSessionRequestPermission, "params": params})
	require.NoError(t, err)
	return frame
}

// resolvedRow is one stored row as a reader of the transcript sees it. It is
// the frame of the agent with the supplement that LeapMux keeps beside it.
func resolvedRow(t *testing.T, message agenttest.Message) map[string]json.RawMessage {
	t.Helper()
	var row map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ResolveMessageContent(agent.MessageContent{Original: message.Content, Supplemental: message.SupplementalContent}), &row))
	return row
}

// The frames of a write_text_file that the reader refused, as Fast Agent
// 0.10.42 sends them when the model streams the call. A probe captured them
// from the installed CLI against a local mock of the model.
//
//   - The opening frame states the title and the kind, and no input. The
//     stream opens the call before the arguments arrive
//     (ACPToolProgressManager._send_stream_start_notification).
//   - An update states the proposed write as a content diff.
//   - The permission request states the input. Its toolCall is a
//     ToolCallUpdate (RequestPermissionRequest.toolCall in the ACP schema).
//   - The failed update replaces the content with the refusal.
//
// The request row is the only row that states the file of the refused call.
// The failed update states the refusal alone, and no stored row holds the diff
// that the failed update replaced.
const (
	fastAgentRefusedWritePath    = "/w/fa-local.txt"
	fastAgentRefusedWriteOpening = `{"sessionUpdate":"tool_call","toolCallId":"fa-write","title":"write_text_file","kind":"edit","status":"pending","content":[]}`
	fastAgentRefusedWriteDiff    = `{"sessionUpdate":"tool_call_update","toolCallId":"fa-write","content":[{"type":"diff","path":"/w/fa-local.txt","newText":"fa-local-content"}]}`
	fastAgentRefusedWriteTitle   = `{"sessionUpdate":"tool_call_update","toolCallId":"fa-write","status":"pending","title":"write_text_file"}`
	fastAgentRefusedWriteAsk     = `{"toolCallId":"fa-write","kind":"edit","status":"pending","title":"write_text_file","rawInput":{"path":"/w/fa-local.txt","content_length":16}}`
	fastAgentRefusedWriteRefusal = `{"sessionUpdate":"tool_call_update","toolCallId":"fa-write","status":"failed","content":[{"type":"content","content":{"type":"text","text":"The user has declined permission to use this tool: acp_filesystem__write_text_file"}}]}`
)

func TestACPPermissionRequestStatesTheInputOfARefusedCall(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteOpening))
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteDiff))
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteTitle))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", fastAgentRefusedWriteAsk))
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteRefusal))

	messages := sink.Messages()
	require.Len(t, messages, 2, "the call stores its request row and its closing row")
	assert.False(t, messages[0].Closing)
	assert.JSONEq(t, fastAgentRefusedWriteOpening, string(messages[0].Content), "the request row keeps the frame of the agent")
	request := resolvedRow(t, messages[0])
	require.Contains(t, request, contracts.ACPSupplementRequestRawInput, "the request row states the input that the permission request gave")
	assert.JSONEq(t, `{"path":"`+fastAgentRefusedWritePath+`","content_length":16}`, string(request[contracts.ACPSupplementRequestRawInput]))
	assert.JSONEq(t, `"write_text_file"`, string(request[contracts.ACPSupplementRequestTitle]))
	assert.JSONEq(t, `"edit"`, string(request[contracts.ACPSupplementRequestKind]))
	assert.True(t, messages[1].Closing)
	assert.JSONEq(t, fastAgentRefusedWriteRefusal, string(messages[1].Content), "the closing row keeps the refusal")
}

// A subagent that runs in a session of its own asks for permission in that
// session. The input reaches the request row in the transcript of the child.
func TestACPPermissionRequestStatesTheInputInTheChildTranscript(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-spawn", ChildAgentKey: "call-spawn", Title: "helper", Status: bgtask.StatusRunning, Spawns: true})
	b.AttachChildSession("child-session", "call-spawn")
	b.HandleOutput(acpNotificationLine(t, "child-session", fastAgentRefusedWriteOpening))
	b.HandleOutput(acpPermissionLine(t, 0, "child-session", fastAgentRefusedWriteAsk))

	child := sink.Child("child-of-call-spawn").Messages()
	require.Len(t, child, 1)
	request := resolvedRow(t, child[0])
	require.Contains(t, request, contracts.ACPSupplementRequestRawInput, "the child's request row states the input that the permission request gave")
	assert.JSONEq(t, `{"path":"`+fastAgentRefusedWritePath+`","content_length":16}`, string(request[contracts.ACPSupplementRequestRawInput]))
	for _, message := range sink.Messages() {
		assert.NotEqual(t, "fa-write", message.SpanID, "the call of the child draws no row in the main transcript")
	}
}

// An agent can open a call before the call states all of its input. OpenCode
// 1.18 opens each call with `rawInput: {}` and `locations: []`, and it fills
// them in an in_progress update. For an apply_patch, that update states the
// patch text and no location. The permission request states the files as
// `locations`. It words the rest for its dialog: its rawInput is the permission
// metadata, and its title is the file. So a permission request fills only an
// input field that no frame of the call stated. Every other field of the
// request row stays the field of the call. The kind of the permission request
// differs from the kind of the call here on purpose.
func TestACPPermissionRequestFillsOnlyTheInputThatTheCallLeftEmpty(t *testing.T) {
	t.Parallel()
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"oc-call","title":"apply_patch","kind":"edit","status":"pending","locations":[],"rawInput":{}}`
	const running = `{"sessionUpdate":"tool_call_update","toolCallId":"oc-call","title":"apply_patch","kind":"edit","status":"in_progress","locations":[],"rawInput":{"patchText":"*** Begin Patch"}}`
	const ask = `{"toolCallId":"oc-call","title":"a.ts","kind":"execute","status":"pending","locations":[{"path":"/w/a.ts"}],"rawInput":{"filepath":"a.ts","diff":"@@"},"content":[{"type":"diff","path":"/w/a.ts","oldText":"a","newText":"b"}]}`
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", opening))
	b.HandleOutput(acpNotificationLine(t, "session-1", running))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", ask))

	messages := sink.Messages()
	require.Len(t, messages, 1)
	request := resolvedRow(t, messages[0])
	assert.JSONEq(t, `[{"path":"/w/a.ts"}]`, string(request[contracts.ACPSupplementRequestLocations]), "the call stated no location, so the permission request states it")
	assert.JSONEq(t, `{"patchText":"*** Begin Patch"}`, string(request[contracts.ACPSupplementRequestRawInput]), "the input stays the call's own")
	assert.JSONEq(t, `"apply_patch"`, string(request[contracts.ACPSupplementRequestTitle]), "the title stays the call's own")
	assert.JSONEq(t, `"edit"`, string(request[contracts.ACPSupplementRequestKind]), "the kind stays the call's own")
	assert.NotContains(t, request, "content", "content is no request field")
}

// Qwen Code 0.24.7 opens a streamed call in its `preparing` phase, with
// `rawInput: {}` and `locations: []`. It sends no update between the opening
// frame and the permission request. The failed update of a refusal states only
// the refusal. So the permission request is the only frame that states the
// file or the command. A probe captured these frames from the installed CLI
// against a local mock of the model. The reader answered the Reject option.
//
// The permission request also words the dialog. Its title is a sentence, and
// its content is the proposed diff of the write. Neither reaches the request
// row.
func TestACPPermissionRequestStatesTheInputOfAStreamedQwenCall(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		opening, ask  string
		wantRawInput  string
		wantLocations string
	}{
		"a write": {
			opening:       `{"sessionUpdate":"tool_call","toolCallId":"call_qwen","status":"pending","title":"WriteFile","content":[],"locations":[],"kind":"edit","rawInput":{},"_meta":{"toolName":"write_file","provenance":"builtin","phase":"preparing"}}`,
			ask:           `{"toolCallId":"call_qwen","status":"pending","title":"Writing to qwen-declined.txt","content":[{"type":"diff","path":"/w/qwen-declined.txt","oldText":"","newText":"qwen-declined-content"}],"locations":[{"path":"/w/qwen-declined.txt"}],"kind":"edit","rawInput":{"file_path":"/w/qwen-declined.txt","content":"qwen-declined-content"},"_meta":{"toolName":"write_file"}}`,
			wantRawInput:  `{"file_path":"/w/qwen-declined.txt","content":"qwen-declined-content"}`,
			wantLocations: `[{"path":"/w/qwen-declined.txt"}]`,
		},
		"a shell command": {
			opening:       `{"sessionUpdate":"tool_call","toolCallId":"call_qwen","status":"pending","title":"Shell","content":[],"locations":[],"kind":"execute","rawInput":{},"_meta":{"toolName":"run_shell_command","provenance":"builtin","phase":"preparing"}}`,
			ask:           `{"toolCallId":"call_qwen","status":"pending","title":"touch /w/qwen-declined.txt","content":[],"locations":[],"kind":"execute","rawInput":{"command":"touch /w/qwen-declined.txt","is_background":false},"_meta":{"toolName":"run_shell_command"}}`,
			wantRawInput:  `{"command":"touch /w/qwen-declined.txt","is_background":false}`,
			wantLocations: `[]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			b.HandleOutput(acpNotificationLine(t, "session-1", testCase.opening))
			b.HandleOutput(acpPermissionLine(t, 0, "session-1", testCase.ask))

			messages := sink.Messages()
			require.Len(t, messages, 1)
			var original map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(messages[0].Content, &original))
			request := resolvedRow(t, messages[0])
			assert.JSONEq(t, testCase.wantRawInput, string(request[contracts.ACPSupplementRequestRawInput]))
			assert.JSONEq(t, testCase.wantLocations, string(request[contracts.ACPSupplementRequestLocations]))
			assert.JSONEq(t, string(original[contracts.ACPSupplementRequestTitle]), string(request[contracts.ACPSupplementRequestTitle]), "the title stays the call's own")
			assert.JSONEq(t, string(original["content"]), string(request["content"]), "the proposed diff of the dialog reaches no row")
		})
	}
}

// The title and the kind identify the call, so a permission request never
// fills them, even for a call whose frames stated no kind. The permission
// request words the title for its dialog, and it can state another kind.
func TestACPPermissionRequestLeavesTheTitleAndTheKindToTheCall(t *testing.T) {
	t.Parallel()
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"call-write","title":"write · /w/a.txt","status":"pending"}`
	const ask = `{"toolCallId":"call-write","title":"Write a file","kind":"other","status":"pending","rawInput":{"path":"/w/a.txt","content":"x"}}`
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", opening))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", ask))

	request := resolvedRow(t, sink.Messages()[0])
	assert.JSONEq(t, `{"path":"/w/a.txt","content":"x"}`, string(request[contracts.ACPSupplementRequestRawInput]), "the call stated no input")
	assert.JSONEq(t, `"write · /w/a.txt"`, string(request[contracts.ACPSupplementRequestTitle]))
	assert.NotContains(t, request, contracts.ACPSupplementRequestKind)
}

// Goose 1.53 opens a write with its title and its input, and no kind. Its
// permission request repeats the title and the input, and states the kind
// `other`. A probe captured these frames from the installed CLI in its
// `approve` mode against a local mock of the model. The reader answered
// reject_once. The call stated its input, so the permission request adds
// nothing, and the request row states no kind.
func TestACPPermissionRequestOfAGooseWriteAddsNothingToTheRequestRow(t *testing.T) {
	t.Parallel()
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"call_goose","title":"write · /w/declined-probe.txt","rawInput":{"path":"/w/declined-probe.txt","content":"declined-probe-content"},"_meta":{"goose":{"toolCall":{"toolName":"write","extensionName":"developer"}}}}`
	const ask = `{"toolCallId":"call_goose","kind":"other","status":"pending","title":"write · /w/declined-probe.txt","rawInput":{"path":"/w/declined-probe.txt","content":"declined-probe-content"}}`
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", opening))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", ask))

	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Empty(t, messages[0].SupplementalContent, "the request row takes no supplement")
	assert.NotContains(t, resolvedRow(t, messages[0]), contracts.ACPSupplementRequestKind)
}

// Gemini CLI 0.62.0 opens a write with the proposed diff and the file in
// `locations`, and it states no raw input. Its permission request repeats the
// opening frame. A probe captured these frames from the installed CLI against
// a local mock of the model. The reader answered the Reject option. The call
// stated the one input field that the request states, so the request changes
// no row.
func TestACPPermissionRequestOfAGeminiWriteChangesNoRow(t *testing.T) {
	t.Parallel()
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"write_file__gemini","status":"pending","title":"Writing to native-denied-file-write.txt","content":[{"type":"diff","path":"/w/native-denied-file-write.txt","oldText":"KEEP\n","newText":"PROPOSED\n","_meta":{"kind":"modify"}}],"locations":[{"path":"/w/native-denied-file-write.txt"}],"kind":"edit"}`
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", opening))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", `{"toolCallId":"write_file__gemini","status":"pending","title":"Writing to native-denied-file-write.txt","content":[{"type":"diff","path":"/w/native-denied-file-write.txt","oldText":"KEEP\n","newText":"PROPOSED\n","_meta":{"kind":"modify"}}],"locations":[{"path":"/w/native-denied-file-write.txt"}],"kind":"edit"}`))

	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Empty(t, messages[0].SupplementalContent, "the request row takes no supplement")
}

// Reasonix 1.38.7 opens a call with its input. It identifies the permission
// request by an approval id (`gate-1`) that is no tool call id of the agent. A
// probe captured these frames from the installed CLI against a local mock of
// the model. No open call matches that id, so the request changes no row.
func TestACPPermissionRequestOfAReasonixCallChangesNoRow(t *testing.T) {
	t.Parallel()
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"call_reasonix","title":"write_file","kind":"edit","status":"pending","rawInput":{"path":"/w/declined-probe.txt","content":"declined-probe-content"},"locations":[{"path":"/w/declined-probe.txt"}]}`
	const ask = `{"toolCallId":"gate-1","title":"write_file /w/declined-probe.txt","kind":"edit","status":"pending","rawInput":{"path":"/w/declined-probe.txt","content":"declined-probe-content"},"locations":[{"path":"/w/declined-probe.txt"}],"_meta":{"reasonix.io":{"approvalId":"1","fresh":false,"subject":"/w/declined-probe.txt","tool":"write_file"}}}`
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", opening))
	b.HandleOutput(acpPermissionLine(t, 1, "session-1", ask))

	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Empty(t, messages[0].SupplementalContent, "the request row takes no supplement")
}

// The permission request of a call that the reader ALLOWS states the input
// first. The fs/write_text_file request of the host states it again, with the
// text. The fold of the permission request reaches the supplement of the
// request row only. So the host still finds the call that states no input, and
// the row ends with the whole input of the write.
func TestACPPermissionRequestLeavesTheWriteOfTheHostToFoldTheWholeInput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fa-local.txt")
	stdin := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, stdin)
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteOpening))
	b.HandleOutput(acpPermissionLine(t, 0, "session-1", `{"toolCallId":"fa-write","kind":"edit","status":"pending","title":"write_text_file","rawInput":{"path":`+jsonString(t, path)+`,"content_length":16}}`))
	request := resolvedRow(t, sink.Messages()[0])
	assert.JSONEq(t, `{"path":`+jsonString(t, path)+`,"content_length":16}`, string(request[contracts.ACPSupplementRequestRawInput]))

	params, err := json.Marshal(acpFSWriteTextFileParams{SessionID: "session-1", Path: path, Content: "fa-local-content"})
	require.NoError(t, err)
	b.handleFSMethod(&providerkit.ParsedLine{ID: json.RawMessage(`1`), Method: acpMethodFSWriteTextFile, Params: params})
	assert.Nil(t, readACPFSWireResponse(t, stdin, `1`).Error)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	request = resolvedRow(t, messages[0])
	assert.JSONEq(t, `{"path":`+jsonString(t, path)+`,"content":"fa-local-content"}`, string(request[contracts.ACPSupplementRequestRawInput]),
		"the input of the host replaces the input of the permission request")
}

// A permission request changes no row unless two conditions hold. It
// identifies a call that a transcript of the agent holds open. It states a
// field that the call left empty.
func TestACPPermissionRequestChangesNoRowWithoutAnOpenCallThatLacksTheField(t *testing.T) {
	t.Parallel()
	const ask = `{"toolCallId":"fa-write","rawInput":{"path":"/w/fa-local.txt"}}`
	for name, testCase := range map[string]struct {
		frames     []string
		permission []byte
	}{
		"a call that never opened": {
			permission: acpPermissionLine(t, 0, "session-1", `{"toolCallId":"never","rawInput":{"path":"/w/fa-local.txt"}}`),
		},
		"a call that ended": {
			frames:     []string{fastAgentRefusedWriteRefusal},
			permission: acpPermissionLine(t, 0, "session-1", ask),
		},
		"a session that the agent does not serve": {
			permission: acpPermissionLine(t, 0, "other-session", ask),
		},
		"a tool call with no id": {
			permission: acpPermissionLine(t, 0, "session-1", `{"rawInput":{"path":"/w/fa-local.txt"}}`),
		},
		"a tool call id that is not a string": {
			permission: acpPermissionLine(t, 0, "session-1", `{"toolCallId":7,"rawInput":{"path":"/w/fa-local.txt"}}`),
		},
		"no tool call": {
			permission: []byte(`{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"session-1"}}`),
		},
		"params that are not an object": {
			permission: []byte(`{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":[]}`),
		},
		"fields that state nothing": {
			permission: acpPermissionLine(t, 0, "session-1", `{"toolCallId":"fa-write","title":"","kind":null,"rawInput":{},"locations":[]}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteOpening))
			for _, frame := range testCase.frames {
				b.HandleOutput(acpNotificationLine(t, "session-1", frame))
			}
			b.HandleOutput(testCase.permission)
			messages := sink.Messages()
			require.NotEmpty(t, messages)
			assert.Equal(t, fastAgentRefusedWriteOpening, string(messages[0].Content))
			assert.Empty(t, messages[0].SupplementalContent, "the request row takes no supplement")
		})
	}
}

// A request that states no session belongs to the main session. The session
// guard of the control request reads it the same way (ServesSession).
func TestACPPermissionRequestWithNoSessionFillsTheMainTranscript(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.HandleOutput(acpNotificationLine(t, "session-1", fastAgentRefusedWriteOpening))
	b.HandleOutput(acpPermissionLine(t, 0, "", fastAgentRefusedWriteAsk))
	request := resolvedRow(t, sink.Messages()[0])
	assert.JSONEq(t, `{"path":"`+fastAgentRefusedWritePath+`","content_length":16}`, string(request[contracts.ACPSupplementRequestRawInput]))
}

func TestRequestFieldStates(t *testing.T) {
	t.Parallel()
	for raw, states := range map[string]bool{
		``:               false,
		`   `:            false,
		`null`:           false,
		`""`:             false,
		`{}`:             false,
		" {\n\t} ":       false,
		`[]`:             false,
		`[ ]`:            false,
		`" "`:            true,
		`"a"`:            true,
		`0`:              true,
		`false`:          true,
		`{"path":"a"}`:   true,
		`[{"path":"a"}]`: true,
		` {"path":"a"} `: true,
		`{"content":""}`: true,
		`[null]`:         true,
	} {
		assert.Equal(t, states, requestFieldStates(json.RawMessage(raw)), "%q", raw)
	}
}

// jsonString spells s as one JSON string.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	encoded, err := json.Marshal(s)
	require.NoError(t, err)
	return string(encoded)
}
