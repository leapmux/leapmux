package dirac

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracNativeTaskletReceiptAdoptsExactChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	a.SetPromptActiveForTest(true)
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	path := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "ui_messages.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"id":"native-assistant-text","content":{"type":"markdown","content":"The native child inspected the parser.","isReasoning":false},"conversationHistoryIndex":0}]`), 0o600))
	a.HandleOutput(diracFrame(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents",
		"title": "Dirac → Witten · Start agents", "kind": "other", "status": "completed",
	}))
	a.archiveWG.Wait()
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1, "the native receipt and root-qualified record identify one real child")
	assert.Equal(t, "Inspect the parser", rows[0].Title)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	require.NotEmpty(t, rows[0].ChildAgentID)
	spawn, err := sink.ChildSpawnSpan(rows[0].ChildAgentID)
	require.NoError(t, err)
	assert.Equal(t, "native-spawn-card", spawn)
	child := sink.Child(rows[0].ChildAgentID)
	require.NotNil(t, child)
	assert.Equal(t, "01M7P10WRK68MMQJW753C9KJ2T", child.LastSessionID())
	var textRecord map[string]json.RawMessage
	for _, row := range child.Messages() {
		var candidate map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(row.Content, &candidate))
		if string(candidate["type"]) == `"leapmux.dirac.message"` {
			textRecord = candidate
		}
	}
	require.NotNil(t, textRecord, "the native child assistant message must be retained")
	assert.JSONEq(t, `{"id":"native-assistant-text","content":{"type":"markdown","content":"The native child inspected the parser.","isReasoning":false},"conversationHistoryIndex":0}`, string(textRecord["message"]))
}

func writeDiracTaskletStore(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"data/acp-session-tasks.json":                                `{"dirac-session":["native-root"]}`,
		"data/acp-session-runtime-configs/dirac-session.json":        `{"version":1,"taskId":"native-root","settings":{}}`,
		"data/tasks/native-root/ui_messages.jsonl":                   `{"offset":0,"type":"create","message":{"id":"native-spawn-card","content":{"type":"card","card":{"id":"native-spawn-card","toolName":"subagents","status":"success","taskletReceipt":{"operation":"spawn","outcome":"accepted","rows":[{"operation":"spawn","taskletId":"Witten","title":"Inspect the parser","role":"default","message":"Inspect the parser and report.","outcome":"accepted"}]}}},"conversationHistoryIndex":1}}` + "\n",
		"data/tasks/native-root/tasklets/Witten/tasklet.json":        `{"id":"Witten","rootTaskId":"native-root","conversationUlid":"01M7P10WRK68MMQJW753C9KJ2T","title":"Inspect the parser","prompt":"Inspect the parser and report.","createdAt":0,"outcome":{"kind":"completed","response":"The parser is correct.","completedAt":1}}`,
		"data/tasks/native-root/tasklets/Witten/tasklet_events.json": `[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"outcome:Witten:completed:1","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"The parser is correct.","completedAt":1}}}]`,
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	}
}

func TestDiracCompletedTaskletReplayDoesNotRequireAClosedRoute(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.NoError(t, err)
	require.Len(t, metadata.Tasklets, 1)
	watch := func() *diracTaskletWatch {
		return &diracTaskletWatch{sessionID: "dirac-session", observed: make(map[string]string), writers: make(map[string]*diracTaskletTranscript)}
	}
	first := watch()
	a.taskletWatch = first
	require.NoError(t, a.publishDiracTasklet(first, metadata.Tasklets[0]))
	a.FlushRegistryObservations()
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	childID := rows[0].ChildAgentID
	messages := sink.Child(childID).Messages()
	second := watch()
	a.taskletWatch = second
	require.NoError(t, a.publishDiracTasklet(second, metadata.Tasklets[0]), "a retained outcome must not block the next tasklet")
	a.FlushRegistryObservations()
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	assert.Equal(t, childID, rows[0].ChildAgentID)
	assert.Equal(t, messages, sink.Child(childID).Messages())
}
