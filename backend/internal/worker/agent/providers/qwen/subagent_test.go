package qwen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The frames of one foreground subagent, as Qwen 0.24.3 sent them (probe
// `acp_tools.jsonl`), with the workspace path shortened.
var foregroundSpawnFrames = []string{
	`{"sessionUpdate":"tool_call","toolCallId":"call_32d1939343","status":"pending","title":"Agent","content":[],"locations":[],"kind":"other","rawInput":{},"_meta":{"toolName":"agent","provenance":"builtin","phase":"preparing","subagentSessionReady":false}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"call_32d1939343","status":"in_progress","title":"Agent: Child probe","content":[],"locations":[],"kind":"other","rawInput":{"description":"Child probe","prompt":"CHILD_TASK: list the files","subagent_type":"general-purpose","run_in_background":false},"_meta":{"toolName":"agent","provenance":"builtin","subagentSessionReady":false}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"call_32d1939343","_meta":{"toolName":"agent","subagentSessionReady":true}}`,
	`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Child thinki"},"_meta":{"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"ng."},"_meta":{"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Child will l"},"_meta":{"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ist."},"_meta":{"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"usage":{"inputTokens":100,"outputTokens":10,"totalTokens":110,"thoughtTokens":4,"cachedReadTokens":0},"durationMs":148,"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"tool_call","toolCallId":"call_cf768dc6c7","status":"pending","title":"list_directory","content":[],"locations":[],"kind":"other","rawInput":{"path":"/ws"},"_meta":{"toolName":"list_directory","provenance":"subagent","parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"call_cf768dc6c7","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Tool \"list_directory\" not found."}}],"_meta":{"toolName":"list_directory","provenance":"subagent","parentToolCallId":"call_32d1939343","subagentType":"general-purpose"},"rawOutput":"Tool \"list_directory\" not found."}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Child done: listed."},"_meta":{"parentToolCallId":"call_32d1939343","subagentType":"general-purpose"}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"call_32d1939343","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Child done: listed."}}],"_meta":{"toolName":"agent","provenance":"builtin"},"rawOutput":{"type":"task_execution","subagentName":"general-purpose","taskDescription":"Child probe","taskPrompt":"CHILD_TASK: list the files","executionMode":"foreground","status":"completed","result":"Child done: listed."}}`,
}

// rawUpdate wraps one update of the test session.
func rawUpdate(t *testing.T, update string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": qwenTestSession, "update": json.RawMessage(update)},
	})
}

// childTexts reads the assembled rows of one child transcript, with their kind.
func childTexts(child *agenttest.Sink) []string {
	var texts []string
	for _, message := range child.Messages() {
		if kind, text, ok := decodeAssembledText(message.Content); ok {
			texts = append(texts, kind+":"+text)
		}
	}
	return texts
}

// spanRows returns the rows of one span in a transcript.
func spanRows(messages []agenttest.Message, spanID string) []agenttest.Message {
	var out []agenttest.Message
	for _, message := range messages {
		if message.SpanID == spanID {
			out = append(out, message)
		}
	}
	return out
}

func TestQwenForegroundSubagentLifecycle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(rawUpdate(t, foregroundSpawnFrames[0]))
	row, ok := sink.BackgroundTask("call_32d1939343")
	require.True(t, ok, "the spawn opens a row at its first frame")
	assert.Equal(t, "Subagent", row.Title, "the first frame states no arguments")
	require.NotEmpty(t, row.ChildAgentID)
	child := sink.Child(row.ChildAgentID)

	for _, update := range foregroundSpawnFrames[1:] {
		a.HandleOutput(rawUpdate(t, update))
	}

	row, _ = sink.BackgroundTask("call_32d1939343")
	assert.Equal(t, "Child probe", row.Title, "the progress frame gives the row its title")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Equal(t, []string{"reasoning:Child thinking.", "text:Child will list.", "text:Child done: listed."}, childTexts(child))
	childTool := spanRows(child.Messages(), "call_cf768dc6c7")
	require.Len(t, childTool, 2, "the child's tool call opens and closes in the child's tab")
	assert.Empty(t, spanRows(sink.Messages(), "call_cf768dc6c7"), "no child tool call reaches the parent")
	assert.Equal(t, []string{"Child done: listed."}, reportTexts(child), "the spawn's result is the child's report")
	assert.Equal(t, 0, sink.SessionInfoCount(), "the child's usage is not the parent's")
}

// reportTexts reads the report notifications of one child transcript.
func reportTexts(child *agenttest.Sink) []string {
	var texts []string
	for _, notification := range child.LeapMuxNotifications() {
		if text, ok := notification["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestQwenFailedSpawnClosesItsRow(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bgtask.Status{"failed": bgtask.StatusFailed, "cancelled": bgtask.StatusStopped} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newQwenAgent(t, nil, nil)
			a.HandleOutput(rawUpdate(t, foregroundSpawnFrames[0]))
			a.HandleOutput(sessionUpdate(t, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call_32d1939343", "status": status}))
			row, ok := sink.BackgroundTask("call_32d1939343")
			require.True(t, ok)
			assert.Equal(t, want, row.Status)
		})
	}
}

func TestQwenForegroundChildStatusFollowsItsResult(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, foregroundSpawnFrames[0]))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_32d1939343","status":"completed","rawOutput":{"type":"task_execution","executionMode":"foreground","status":"failed","result":""}}`))
	row, _ := sink.BackgroundTask("call_32d1939343")
	assert.Equal(t, bgtask.StatusFailed, row.Status, "a spawn call that completed can carry a child that failed")
}

func TestQwenUpdateTaggedWithAnUnknownSpawnStaysInTheParent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Orphan text."},"_meta":{"parentToolCallId":"call_unknown"}}`))
	a.FinishPromptRequestForTest(qwenTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Equal(t, []string{"text:Orphan text."}, childTexts(&sink.Sink), "text that no child claims stays where the reader sees it")
}

// backgroundLaunchResult is the spawn result of a background subagent whose
// transcript is at path.
func backgroundLaunchResult(t *testing.T, path string) []byte {
	t.Helper()
	return sessionUpdate(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_686a7e3e21", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text",
			"text": "Background agent launched successfully.\ntask_id: general-purpose-call_686a7e3e21\noutput_file: " + path + " (for review after the completion notification, not for polling)"}}},
		"_meta":     map[string]any{"toolName": "agent", "provenance": "builtin"},
		"rawOutput": map[string]any{"type": "task_execution", "executionMode": "background", "status": "background"},
	})
}

// backgroundDone is Qwen's notice that a background subagent finished.
func backgroundDone(t *testing.T, status string) []byte {
	t.Helper()
	return metaChunk(t, `Background agent "general-purpose: Background child" completed.`, map[string]any{
		"source": "background_task_completed", "qwenDiscreteMessage": true,
		"backgroundTask": map[string]any{
			"taskId": "general-purpose-call_686a7e3e21", "status": status, "kind": "agent",
			"toolUseId": "call_686a7e3e21", "description": "general-purpose: Background child",
		},
	})
}

// subagentTranscriptPath is where Qwen keeps the transcript of the test's
// background child.
func subagentTranscriptPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "projects", "-ws", "subagents", "parent-session")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return filepath.Join(dir, "agent-general-purpose-call_686a7e3e21.jsonl")
}

func appendRecords(t *testing.T, path string, records ...string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	for _, record := range records {
		_, err := file.WriteString(record + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())
}

func TestQwenBackgroundSubagentStreamsItsTranscript(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "transcript")
	defer trap.Close()
	a, sink, _ := newQwenAgent(t, clock, nil)
	path := subagentTranscriptPath(t)

	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent","phase":"preparing"}}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_686a7e3e21","status":"in_progress","rawInput":{"description":"Background child","prompt":"CHILD_TASK: list the files","subagent_type":"general-purpose"},"_meta":{"toolName":"agent"}}`))
	// The trap holds the reader's ticker until the test releases it, and the
	// reader starts on the goroutine that reads the spawn's result, so that
	// read runs beside the release.
	launch := backgroundLaunchResult(t, path)
	read := make(chan struct{})
	go func() {
		defer close(read)
		a.HandleOutput(launch)
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-read

	row, ok := sink.BackgroundTask("call_686a7e3e21")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a background child outlives its spawn call")
	assert.Equal(t, "Running in the background", row.ActiveForm)
	child := sink.Child(row.ChildAgentID)

	appendRecords(t, path, backgroundRecords[:3]...)
	clock.Advance(transcriptPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool { return len(spanRows(child.Messages(), "call_22ab86821a")) == 1 })

	appendRecords(t, path, backgroundRecords[3:]...)
	a.HandleOutput(backgroundDone(t, "completed"))

	row, _ = sink.BackgroundTask("call_686a7e3e21")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Equal(t, []string{"reasoning:Child thinking.", "text:Child will list.", "text:Child done: listed."}, childTexts(child),
		"the records written before the notice all reach the tab, the last ones by the final read")
	assert.Len(t, spanRows(child.Messages(), "call_22ab86821a"), 2)
	assert.Equal(t, []string{"send_message: also check the tests"}, childUserMessages(child), "a message the parent sent to the running child reaches its tab")
}

// childUserMessages reads the messages that the parent sent to one running
// child. The spawn's prompt opens the tab with no mark, so it is not one.
func childUserMessages(child *agenttest.Sink) []string {
	var texts []string
	for _, message := range child.Messages() {
		if message.MarkType != leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE {
			continue
		}
		var user struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(message.Content, &user) == nil && user.Content != "" {
			texts = append(texts, user.Content)
		}
	}
	return texts
}

func TestQwenBackgroundSpawnWithNoUsablePathStartsNoReader(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(backgroundLaunchResult(t, "/etc/passwd.jsonl"))

	a.stateMu.Lock()
	assert.Empty(t, a.children.tails, "a path outside a subagents directory is refused")
	a.stateMu.Unlock()
	row, _ := sink.BackgroundTask("call_686a7e3e21")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "the row still follows the child")
	a.HandleOutput(backgroundDone(t, "failed"))
	row, _ = sink.BackgroundTask("call_686a7e3e21")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestQwenBackgroundShellRows(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_sh","status":"pending","title":"Shell","kind":"execute","rawInput":{},"_meta":{"toolName":"run_shell_command","phase":"preparing"}}`))
	// The permission request states the arguments that the closing frame omits.
	a.HandleOutput(frame(t, map[string]any{"id": 3, "method": "session/request_permission", "params": map[string]any{
		"sessionId": qwenTestSession,
		"options":   []any{map[string]any{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"}},
		"toolCall": map[string]any{"toolCallId": "call_sh", "kind": "execute",
			"rawInput": map[string]any{"command": "npm run dev", "is_background": true},
			"_meta":    map[string]any{"toolName": "run_shell_command"}},
	}}))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_sh","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Background shell started.\nid: bg_1a2b3c4d\npid: 42\noutput file: /tmp/x"}}],"_meta":{"toolName":"run_shell_command"}}`))

	row, ok := sink.BackgroundTask("shell:bg_1a2b3c4d")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, "npm run dev", row.Title)
	assert.True(t, row.TitleIsCommand)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	a.HandleOutput(metaChunk(t, "Background shell finished.", map[string]any{
		"source": "background_task_completed", "backgroundTask": map[string]any{"taskId": "bg_1a2b3c4d", "status": "failed", "kind": "shell"},
	}))
	row, _ = sink.BackgroundTask("shell:bg_1a2b3c4d")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestQwenPromotedShellTakesARow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_sh","status":"pending","kind":"execute","rawInput":{"command":"sleep 99","description":"Wait a while"},"_meta":{"toolName":"run_shell_command"}}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_sh","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Foreground command \"sleep 99\" promoted to background as bg_9f."}}],"_meta":{"toolName":"run_shell_command"}}`))
	row, ok := sink.BackgroundTask("shell:bg_9f")
	require.True(t, ok)
	assert.Equal(t, "Wait a while", row.Title)
	assert.False(t, row.TitleIsCommand, "a description is prose")
}

func TestQwenForegroundShellTakesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_sh","status":"pending","kind":"execute","rawInput":{"command":"ls"},"_meta":{"toolName":"run_shell_command"}}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_sh","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Command: ls\nExit Code: 0"}}],"_meta":{"toolName":"run_shell_command"}}`))
	assert.Empty(t, sink.BackgroundTasks())
}

func TestQwenWorkflowRunKeepsARow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_wf","status":"pending","title":"Workflow","kind":"other","rawInput":{"name":"triage"},"_meta":{"toolName":"workflow"}}`))
	row, ok := sink.BackgroundTask("call_wf")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, row.Kind)
	assert.Equal(t, "triage", row.Title)
	assert.Equal(t, "call_wf", row.GroupKey)
	assert.Equal(t, "triage", row.GroupLabel)

	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"in_progress"}`))
	row, _ = sink.BackgroundTask("call_wf")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"completed"}`))
	row, _ = sink.BackgroundTask("call_wf")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
}

func TestQwenWorkflowTitleFallsBack(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "scripts/check.js", workflowObservation("c", json.RawMessage(`{"scriptPath":"scripts/check.js"}`)).Title)
	assert.Equal(t, "Workflow", workflowObservation("c", json.RawMessage(`{"script":"phase()"}`)).Title)
	assert.Equal(t, "Workflow", workflowObservation("c", nil).Title)
}

// Stop and Wait end every reader through stopAllBackgroundTranscripts. The
// test peer's process never exits, so Stop itself cannot run here: it waits for
// that exit.
func TestQwenStoppingTheReadersEndsEachLoop(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(backgroundLaunchResult(t, subagentTranscriptPath(t)))
	a.stateMu.Lock()
	require.Len(t, a.children.tails, 1)
	tail := a.children.tails["call_686a7e3e21"]
	a.stateMu.Unlock()

	a.stopAllBackgroundTranscripts()

	a.stateMu.Lock()
	assert.Empty(t, a.children.tails)
	a.stateMu.Unlock()
	select {
	case <-tail.stopped:
	default:
		t.Fatal("the reader's loop still runs")
	}
	a.stopAllBackgroundTranscripts()
}

// The transcript path that a background spawn states can hold a space: a home
// directory or a runtime directory often does, on Windows most of all. Qwen
// ends the path with a sentence of its own, which is not part of it.
func TestQwenBackgroundTranscriptPathMayHoldASpace(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	dir := filepath.Join(t.TempDir(), "Jane Doe", "projects", "-ws", "subagents", "parent-session")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "agent-general-purpose-call_686a7e3e21.jsonl")

	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(backgroundLaunchResult(t, path))

	a.stateMu.Lock()
	tail := a.children.tails["call_686a7e3e21"]
	a.stateMu.Unlock()
	require.NotNil(t, tail, "the child's transcript is read")
	assert.Equal(t, path, tail.path)
}

// qwenTaskResponder answers Qwen's task list with tasks, and each task cancel
// with cancel.
func qwenTaskResponder(tasks, cancel string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case "qwen/status/session/tasks":
			return agenttest.RPCReply{Result: json.RawMessage(`{"v":1,"sessionId":"session-1","now":1,"tasks":` + tasks + `}`)}
		case "qwen/control/session/task/cancel":
			return agenttest.RPCReply{Result: json.RawMessage(cancel)}
		case "session/new":
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// qwenTaskFixture is Qwen's task list: a foreground subagent that runs, a
// background one that Qwen paused, a subagent that completed, a background
// command, and a monitor.
const qwenTaskFixture = `[
	{"kind":"agent","id":"general-purpose-call_1","toolUseId":"call_1","status":"running","isBackgrounded":false},
	{"kind":"agent","id":"explore-call_2","toolUseId":"call_2","status":"paused","isBackgrounded":true},
	{"kind":"agent","id":"general-purpose-call_3","toolUseId":"call_3","status":"completed","isBackgrounded":true},
	{"kind":"shell","id":"bg_1a2b","status":"running"},
	{"kind":"monitor","id":"mon_7","status":"running"},
	{"kind":"shell","id":"bg_done","status":"completed"}
]`

// childInterrupter returns the agent as the interface that the stop of a
// child tab reaches it through.
func childInterrupter(t *testing.T, a *Agent) agent.ChildInterrupter {
	t.Helper()
	interrupter, ok := any(a).(agent.ChildInterrupter)
	require.True(t, ok, "a Qwen subagent can be stopped from its own tab")
	return interrupter
}

// taskCancels returns the params of each task cancel that the agent sent.
func taskCancels(requests []agenttest.RecordedRequest) []map[string]any {
	var out []map[string]any
	for _, request := range requestsFor(requests, "qwen/control/session/task/cancel") {
		out = append(out, request.Params)
	}
	return out
}

// Qwen stops one subagent, in the foreground or the background, through the
// task cancel of its session. The task id is Qwen's own, and no frame of a
// foreground spawn states it, so the stop reads it from Qwen's task list by the
// tool call that spawned the subagent, which is the row key.
func TestQwenInterruptChildCancelsTheTaskOfTheSpawn(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenTaskResponder(qwenTaskFixture, `{"cancelled":true,"status":"running"}`))
	interrupter := childInterrupter(t, a)

	require.NoError(t, interrupter.InterruptChild("call_1"))
	require.NoError(t, interrupter.InterruptChild("call_2"))
	syncPeer(t, a)

	assert.Equal(t, []map[string]any{
		{"sessionId": qwenTestSession, "taskId": "general-purpose-call_1", "taskKind": "agent"},
		{"sessionId": qwenTestSession, "taskId": "explore-call_2", "taskKind": "agent"},
	}, taskCancels(requests()))
	lists := requestsFor(requests(), "qwen/status/session/tasks")
	require.NotEmpty(t, lists)
	assert.Equal(t, qwenTestSession, lists[0].Params["sessionId"])
}

// A subagent that already ended leaves nothing to stop, and a race that ends
// it between the list and the cancel changes nothing either.
func TestQwenInterruptChildOfAnEndedSubagentSendsNoCancel(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenTaskResponder(qwenTaskFixture, `{"cancelled":false,"reason":"not_running","status":"completed"}`))

	require.NoError(t, childInterrupter(t, a).InterruptChild("call_3"))
	syncPeer(t, a)
	assert.Empty(t, taskCancels(requests()), "a completed subagent takes no cancel")

	require.NoError(t, childInterrupter(t, a).InterruptChild("call_1"), "a subagent that ended before the cancel arrived needs no stop")
}

func TestQwenInterruptChildOfAnUnknownSpawnFails(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenTaskResponder(qwenTaskFixture, `{"cancelled":true}`))

	err := childInterrupter(t, a).InterruptChild("call_unknown")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "call_unknown")
	syncPeer(t, a)
	assert.Empty(t, taskCancels(requests()))
}

func TestQwenInterruptChildReportsAFailedTaskList(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == "qwen/status/session/tasks" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"boom"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	require.Error(t, childInterrupter(t, a).InterruptChild("call_1"))
}

// A context clear stops the background work of the outgoing session: Qwen's
// session/cancel ends the turn and its goal and notification turns, and leaves
// background subagents, commands and monitors running. Qwen advertises no
// session/close, so the clear cancels each running task through Qwen's own
// task cancel.
func TestQwenClearContextCancelsTheBackgroundTasksOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenTaskResponder(qwenTaskFixture, `{"cancelled":true}`))

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)

	want := []map[string]any{
		{"sessionId": qwenTestSession, "taskId": "general-purpose-call_1", "taskKind": "agent"},
		{"sessionId": qwenTestSession, "taskId": "explore-call_2", "taskKind": "agent"},
		{"sessionId": qwenTestSession, "taskId": "bg_1a2b", "taskKind": "shell"},
		{"sessionId": qwenTestSession, "taskId": "mon_7", "taskKind": "monitor"},
	}
	// The cancels go out detached, after the list answers.
	testutil.RequireEventually(t, func() bool { return len(taskCancels(requests())) == len(want) })
	assert.ElementsMatch(t, want, taskCancels(requests()))
	lists := requestsFor(requests(), "qwen/status/session/tasks")
	require.Len(t, lists, 1)
	assert.Equal(t, qwenTestSession, lists[0].Params["sessionId"], "the list is of the outgoing session")
	assert.Empty(t, requestsFor(requests(), "session/close"), "Qwen advertises no session/close")
}

func TestQwenToolInputsRemember(t *testing.T) {
	t.Parallel()
	var inputs toolInputs
	inputs.remember("", contracts.QwenToolAgent, json.RawMessage(`{"prompt":"x"}`))
	assert.Empty(t, inputs.byCall, "a frame that states no call records nothing")

	inputs.remember("call_1", contracts.QwenToolAgent, json.RawMessage(`{"prompt":"first"}`))
	for _, empty := range []string{``, `{}`, `null`} {
		inputs.remember("call_1", "", json.RawMessage(empty))
	}
	assert.Equal(t, toolInput{name: contracts.QwenToolAgent, rawInput: json.RawMessage(`{"prompt":"first"}`)}, inputs.byCall["call_1"],
		"a frame that states no name and no arguments leaves what an earlier frame stated")

	inputs.remember("call_1", contracts.QwenToolWorkflow, json.RawMessage(`{"prompt":"second"}`))
	assert.Equal(t, toolInput{name: contracts.QwenToolWorkflow, rawInput: json.RawMessage(`{"prompt":"second"}`)}, inputs.byCall["call_1"])
}

func TestQwenToolNameAndChildRouteReadOnlyAString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, contracts.QwenToolAgent, qwenToolName(json.RawMessage(`{"toolName":"agent","phase":"x"}`)))
	for _, meta := range []string{``, `null`, `{}`, `{"toolName":7}`, `"agent"`, `not json`} {
		assert.Empty(t, qwenToolName(json.RawMessage(meta)), meta)
	}

	assert.Equal(t, "call_1", childUpdateRoute(qwenTestSession, map[string]json.RawMessage{contracts.QwenMetaParentToolCallId: json.RawMessage(`"call_1"`)}))
	for _, parent := range []string{`7`, `{"id":"call_1"}`, `not json`} {
		assert.Empty(t, childUpdateRoute(qwenTestSession, map[string]json.RawMessage{contracts.QwenMetaParentToolCallId: json.RawMessage(parent)}), parent)
	}
	assert.Empty(t, childUpdateRoute(qwenTestSession, nil), "an update with no parent stays in the parent")
}

// A spawn that completes with no task result of its own reports the text of
// its content, which is what the child answered.
func TestQwenCompletedSpawnWithNoResultReportsItsContent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, foregroundSpawnFrames[0]))
	row, ok := sink.BackgroundTask("call_32d1939343")
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)

	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_32d1939343","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Plain answer."}}]}`))

	row, _ = sink.BackgroundTask("call_32d1939343")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Equal(t, []string{"Plain answer."}, reportTexts(child))
}

// A background spawn whose call failed launched no child, so its row closes
// and no reader starts.
func TestQwenFailedBackgroundLaunchClosesTheRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_686a7e3e21", "status": "failed",
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "output_file: " + subagentTranscriptPath(t)}}},
		"rawOutput": map[string]any{"type": "task_execution", "executionMode": "background", "status": "background"},
	}))

	row, ok := sink.BackgroundTask("call_686a7e3e21")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	a.stateMu.Lock()
	assert.Empty(t, a.children.tails)
	a.stateMu.Unlock()
}

// Qwen can repeat the closing frame of a background spawn. The child keeps one
// reader, so no record reaches its tab twice.
func TestQwenRepeatedBackgroundLaunchKeepsOneReader(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	path := subagentTranscriptPath(t)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(backgroundLaunchResult(t, path))
	a.stateMu.Lock()
	first := a.children.tails["call_686a7e3e21"]
	a.stateMu.Unlock()
	require.NotNil(t, first)

	a.HandleOutput(backgroundLaunchResult(t, path))

	a.stateMu.Lock()
	assert.Len(t, a.children.tails, 1)
	assert.Same(t, first, a.children.tails["call_686a7e3e21"], "the running reader stays")
	a.stateMu.Unlock()
	row, _ := sink.BackgroundTask("call_686a7e3e21")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

// A shell call that failed left nothing running, whatever its text states.
func TestQwenFailedShellCallTakesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_sh","status":"pending","kind":"execute","rawInput":{"command":"npm run dev"},"_meta":{"toolName":"run_shell_command"}}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_sh","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Background shell started.\nid: bg_dead\n"}}],"_meta":{"toolName":"run_shell_command"}}`))

	assert.Empty(t, sink.BackgroundTasks())
}

func TestQwenBackgroundShellTitleFallsBack(t *testing.T) {
	t.Parallel()
	var tcu acp.ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"call_sh","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Background shell started.\nid: bg_1\n"}}]}`), &tcu))
	for _, rawInput := range []string{``, `{}`, `{"command":"  ","description":" "}`, `not json`} {
		observation := backgroundShellObservation(tcu, json.RawMessage(rawInput))
		require.NotNil(t, observation, rawInput)
		assert.Equal(t, "shell:bg_1", observation.RowKey)
		assert.Equal(t, "Background shell", observation.Title, rawInput)
		assert.False(t, observation.TitleIsCommand, "a fallback title is no command")
	}

	require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"call_sh","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Command: ls\nid: bg_1"}}]}`), &tcu))
	assert.Nil(t, backgroundShellObservation(tcu, json.RawMessage(`{"command":"ls"}`)), "an id that no start line states is not a background command")
}

func TestQwenWorkflowRunThatFailsClosesItsRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_wf","status":"pending","title":"Workflow","kind":"other","rawInput":{"name":"triage"},"_meta":{"toolName":"workflow"}}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"failed"}`))

	row, ok := sink.BackgroundTask("call_wf")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestQwenInterruptChildReportsAnUnreadableTaskList(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenTaskListMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":"not a list"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	err := childInterrupter(t, a).InterruptChild("call_1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "task list")
	syncPeer(t, a)
	assert.Empty(t, taskCancels(requests()))
}

func TestQwenInterruptChildReportsAFailedCancel(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case qwenTaskListMethod:
			return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":` + qwenTaskFixture + `}`)}
		case qwenTaskCancelMethod:
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"boom"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	err := childInterrupter(t, a).InterruptChild("call_1")

	require.Error(t, err, "a cancel that Qwen could not run is a failed stop")
	assert.Contains(t, err.Error(), "stop the Qwen subagent")
}

// abortedForegroundResult is the closing frame of a foreground subagent that
// Qwen's task cancel stopped in the middle of a model request, as Qwen 0.24.4
// sent it in the E2E run of `124-qwen-subagent-registry`. The aborted request
// throws inside the subagent, so the spawn's result states `failed`, although
// the reader stopped it.
func abortedForegroundResult(t *testing.T, toolCallID string) []byte {
	t.Helper()
	return sessionUpdate(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": toolCallID, "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Subagent execution failed."}}},
		"_meta":   map[string]any{"toolName": "agent", "provenance": "builtin"},
		"rawOutput": map[string]any{
			"type": "task_execution", "subagentName": "general-purpose", "taskDescription": "Count to one hundred",
			"taskPrompt": "Count slowly to one hundred.", "executionMode": "foreground", "subagentSessionReady": true,
			"status": "failed", "terminateReason": "Failed to run subagent: Request was aborted.",
		},
	})
}

// openForegroundSpawn opens the row of a foreground spawn with the first frame
// that Qwen sends for it.
func openForegroundSpawn(t *testing.T, a *Agent, toolCallID string) {
	t.Helper()
	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": toolCallID, "status": "pending", "title": "Agent",
		"kind": "other", "rawInput": map[string]any{}, "_meta": map[string]any{"toolName": "agent"},
	}))
}

// A failure that the stop of a child tab caused is a stop. Qwen reports the
// subagent that its task cancel aborted as failed, so the provider reads the
// status of the closing row through the stop that it sent. Only a stop that
// Qwen carried out changes the status: a cancel that failed, or one that
// found the subagent over already, leaves Qwen's own verdict.
func TestQwenStoppedForegroundChildClosesAsStopped(t *testing.T) {
	t.Parallel()
	cancelled := func(agenttest.RecordedRequest) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"cancelled":true,"status":"running"}`)}
	}
	for _, tc := range []struct {
		name string
		// stop is the answer of Qwen's task cancel, or nil for no stop.
		stop    func(agenttest.RecordedRequest) agenttest.RPCReply
		stopErr bool
		want    bgtask.Status
	}{
		{name: "the stop that Qwen carried out", stop: cancelled, want: bgtask.StatusStopped},
		{name: "no stop", want: bgtask.StatusFailed},
		{
			name: "a stop that found the subagent over",
			stop: func(agenttest.RecordedRequest) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{"cancelled":false,"reason":"not_running","status":"failed"}`)}
			},
			want: bgtask.StatusFailed,
		},
		{
			name: "a stop whose answer states nothing readable",
			stop: func(agenttest.RecordedRequest) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`"done"`)}
			},
			want: bgtask.StatusFailed,
		},
		{
			name: "a stop that Qwen could not run",
			stop: func(agenttest.RecordedRequest) agenttest.RPCReply {
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"boom"}`)}
			},
			stopErr: true,
			want:    bgtask.StatusFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				switch request.Method {
				case qwenTaskListMethod:
					return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":` + qwenTaskFixture + `}`)}
				case qwenTaskCancelMethod:
					return tc.stop(request)
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			openForegroundSpawn(t, a, "call_1")
			if tc.stop != nil {
				err := childInterrupter(t, a).InterruptChild("call_1")
				if tc.stopErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}

			a.HandleOutput(abortedForegroundResult(t, "call_1"))

			row, ok := sink.BackgroundTask("call_1")
			require.True(t, ok)
			assert.Equal(t, tc.want, row.Status)
		})
	}
}

// The stop belongs to the one subagent that it stopped. A subagent that
// finishes its work before the cancel reaches it keeps its completion, and a
// later subagent that fails on its own stays failed.
func TestQwenStopOfAChildChangesNoOtherVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, qwenTaskResponder(qwenTaskFixture, `{"cancelled":true,"status":"running"}`))
	openForegroundSpawn(t, a, "call_1")
	require.NoError(t, childInterrupter(t, a).InterruptChild("call_1"))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"completed","rawOutput":{"type":"task_execution","executionMode":"foreground","status":"completed","result":"Done."}}`))
	row, _ := sink.BackgroundTask("call_1")
	assert.Equal(t, bgtask.StatusCompleted, row.Status, "the work finished before the stop")

	openForegroundSpawn(t, a, "call_9")
	a.HandleOutput(abortedForegroundResult(t, "call_9"))
	row, _ = sink.BackgroundTask("call_9")
	assert.Equal(t, bgtask.StatusFailed, row.Status, "nobody stopped this subagent")

	a.stateMu.Lock()
	assert.Empty(t, a.children.stopped, "each close takes the record of its stop")
	a.stateMu.Unlock()
}

// A background subagent ends with Qwen's notice, which can state a failure for
// the same abort. The stop decides there too.
func TestQwenStoppedBackgroundChildClosesAsStopped(t *testing.T) {
	t.Parallel()
	const tasks = `[{"kind":"agent","id":"general-purpose-call_686a7e3e21","toolUseId":"call_686a7e3e21","status":"running","isBackgrounded":true}]`
	a, sink, _ := newQwenAgent(t, nil, qwenTaskResponder(tasks, `{"cancelled":true,"status":"running"}`))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	// A path outside a subagents directory starts no reader, and the row still
	// follows the child: the subject here is the row's end alone.
	a.HandleOutput(backgroundLaunchResult(t, "/etc/passwd.jsonl"))
	require.NoError(t, childInterrupter(t, a).InterruptChild("call_686a7e3e21"))

	a.HandleOutput(backgroundDone(t, "failed"))

	row, ok := sink.BackgroundTask("call_686a7e3e21")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestQwenTaskRunsAndCancellable(t *testing.T) {
	t.Parallel()
	for status, runs := range map[string]bool{"running": true, "paused": true, "completed": false, "failed": false, "cancelled": false, "": false} {
		assert.Equal(t, runs, qwenTask{Status: status}.runs(), status)
	}
	for _, tc := range []struct {
		task qwenTask
		want bool
	}{
		{task: qwenTask{Kind: "agent", ID: "a"}, want: true},
		{task: qwenTask{Kind: "shell", ID: "bg_1"}, want: true},
		{task: qwenTask{Kind: "monitor", ID: "mon_1"}, want: true},
		{task: qwenTask{Kind: "agent"}, want: false},
		{task: qwenTask{Kind: "workflow", ID: "wf_1"}, want: false},
		{task: qwenTask{Kind: "future-kind", ID: "x"}, want: false},
	} {
		assert.Equal(t, tc.want, tc.task.cancellable(), "%+v", tc.task)
	}
}

// The retirement of a session cancels only the tasks that Qwen's cancel can
// stop. These take no cancel:
//
//   - A running workflow.
//   - A task with no id.
//   - A task of a kind that this build does not know.
//   - A task that no longer runs.
func TestQwenRetiredSessionCancelsOnlyWhatQwenCanStop(t *testing.T) {
	t.Parallel()
	// The one task that takes a cancel is LAST, so the cancels are complete once
	// it arrives: the agent sends them in list order.
	tasks := `[
		{"kind":"workflow","id":"wf_1","status":"running"},
		{"kind":"agent","id":"","toolUseId":"call_9","status":"running"},
		{"kind":"future-kind","id":"x_1","status":"running"},
		{"kind":"shell","id":"bg_done","status":"completed"},
		{"kind":"monitor","id":"mon_7","status":"running"}
	]`
	a, _, requests := newQwenAgent(t, nil, qwenTaskResponder(tasks, `{"cancelled":true}`))

	_, err := a.ClearContext()
	require.NoError(t, err)

	testutil.RequireEventually(t, func() bool { return len(taskCancels(requests())) > 0 })
	syncPeer(t, a)
	assert.Equal(t, []map[string]any{{"sessionId": qwenTestSession, "taskId": "mon_7", "taskKind": "monitor"}}, taskCancels(requests()))
}
