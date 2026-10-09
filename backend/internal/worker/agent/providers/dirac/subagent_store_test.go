package dirac

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const diracArchiveSessionID = "session-1"

type diracSwapFileOpener struct {
	*os.Root
	beforeOpen func()
}

type diracCloseFaultRoot struct {
	sessionstore.ArchiveRoot
	closeErr error
}

func (r diracCloseFaultRoot) Close() error {
	return errors.Join(r.ArchiveRoot.Close(), r.closeErr)
}

func TestReadDiracChildArchiveReportsReadAndCloseFaults(t *testing.T) {
	root, err := sessionstore.OpenArchiveRoot(t.TempDir())
	require.NoError(t, err)
	closeErr := errors.New("injected dirac root close failure")
	archive, err := readDiracChildArchiveFromRoot(
		diracCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, diracArchiveSessionID,
		&diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"})
	assert.Empty(t, archive)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, err, closeErr)
}

func TestReadDiracChildArchiveClearsDataAfterCloseFault(t *testing.T) {
	dir := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, dir, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	writeDiracArchive(t, dir, "my-task", "my-run", filepath.Join("my-run", "transcript.md"), &child)
	root, err := sessionstore.OpenArchiveRoot(dir)
	require.NoError(t, err)
	closeErr := errors.New("injected dirac root close failure")
	archive, err := readDiracChildArchiveFromRoot(
		diracCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, diracArchiveSessionID, &child)
	assert.Empty(t, archive)
	require.ErrorIs(t, err, closeErr)
}

func (opener *diracSwapFileOpener) Open(path string) (*os.File, error) {
	if opener.beforeOpen != nil {
		beforeOpen := opener.beforeOpen
		opener.beforeOpen = nil
		beforeOpen()
	}
	return opener.Root.Open(path)
}

func diracRecordBlock(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	return "## record\n\n```json\n" + string(raw) + "\n```\n\n"
}

func diracEventBlock(t *testing.T, sequence int, kind string, details any) string {
	t.Helper()
	raw, err := json.MarshalIndent(details, "", "  ")
	require.NoError(t, err)
	return fmt.Sprintf("## 2026-01-01T00:00:00Z · event %d · %s\n\n```json\n%s\n```\n\n", sequence, kind, raw)
}

func writeDiracArchive(t *testing.T, root, taskID, runID, transcriptPath string, child *diracChildState) string {
	t.Helper()
	store := filepath.Join(root, "data", "tasks", taskID, "subagents")
	require.NoError(t, os.MkdirAll(filepath.Join(store, runID), 0o755))
	index := diracArchiveIndexRecord{TaskID: taskID, RunID: runID, Transcript: transcriptPath, Status: "completed"}
	index.Agent.ID, index.Agent.Name = child.agentID, child.agentName
	file, err := os.OpenFile(filepath.Join(store, "index.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = file.WriteString(diracRecordBlock(t, index))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	header := diracArchiveHeader{TaskID: taskID, RunID: runID, Prompt: child.prompt}
	header.Agent.ID, header.Agent.Name = child.agentID, child.agentName
	path := filepath.Join(store, runID, "transcript.md")
	transcript := "# Subagent transcript\n\n" + diracRecordBlock(t, header) +
		diracEventBlock(t, 1, "assistant_text", map[string]string{"text": "ARCHIVED_CHILD_TEXT"}) +
		diracEventBlock(t, 2, "tool_call", map[string]any{"toolUseId": "read-1", "name": "read_file", "input": map[string]string{"path": "note.txt"}}) +
		diracEventBlock(t, 3, "tool_result", map[string]any{"toolUseId": "read-1", "name": "read_file", "result": "ARCHIVED_TOOL_RESULT"}) +
		diracEventBlock(t, 4, "terminal", map[string]string{"result": "ARCHIVED_CHILD_DONE"})
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o644))
	return path
}

func TestReadDiracChildArchiveUsesTheCurrentSessionTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{
		{ID: "other-task", ULID: "other-session", TS: 2},
		{ID: "my-task", ULID: diracArchiveSessionID, TS: 1},
	})
	writeDiracArchive(t, root, "other-task", "other-run", filepath.Join("other-run", "transcript.md"), &child)
	writeDiracArchive(t, root, "my-task", "my-run", filepath.Join("my-run", "transcript.md"), &child)

	archive, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	require.NoError(t, err)
	require.Len(t, archive, 4)
	assert.Equal(t, []string{"assistant_text", "tool_call", "tool_result", "terminal"}, []string{archive[0].Kind, archive[1].Kind, archive[2].Kind, archive[3].Kind})
	assert.Contains(t, string(archive[0].Details), "ARCHIVED_CHILD_TEXT")
	assert.Contains(t, string(archive[2].Details), "ARCHIVED_TOOL_RESULT")
}

func TestReadDiracChildArchiveRejectsSwappedTasksDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	writeDiracArchive(t, outside, "my-task", "outside-run", filepath.Join("outside-run", "transcript.md"), &child)
	tasks := filepath.Join(root, "data", "tasks")
	require.NoError(t, os.MkdirAll(tasks, 0o755))
	require.NoError(t, os.Rename(tasks, tasks+"-prior"))
	if err := os.Symlink(filepath.Join(outside, "data", "tasks"), tasks); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	require.Error(t, err, "a swapped tasks directory must not read outside the Dirac root")
}

func TestReadDiracChildArchiveRejectsInRootAncestor(t *testing.T) {
	root := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	transcript := writeDiracArchive(t, root, "my-task", "my-run", filepath.Join("my-run", "transcript.md"), &child)
	tasks := filepath.Join(root, "data", "tasks")
	alternate := tasks + "-alternate"
	require.NoError(t, os.Rename(tasks, alternate))
	transcript = strings.Replace(transcript, tasks, alternate, 1)
	raw, err := os.ReadFile(transcript)
	require.NoError(t, err)
	substitute := strings.Replace(string(raw), "ARCHIVED_CHILD_TEXT", "SUBSTITUTE_CHILD_TEXT", 1)
	require.NotEqual(t, string(raw), substitute)
	require.NoError(t, os.WriteFile(transcript, []byte(substitute), 0o644))
	if linkErr := os.Symlink(filepath.Base(alternate), tasks); linkErr != nil {
		t.Skipf("symlinks are unavailable: %v", linkErr)
	}

	archive, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	if len(archive) > 0 {
		assert.NotContains(t, string(archive[0].Details), "SUBSTITUTE_CHILD_TEXT")
	}
	require.Error(t, err, "an in-root tasks link cannot supply a child archive")
	assert.Empty(t, archive)
}

func TestReadDiracRegularFileRejectsSwapBetweenStatAndOpen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
	}{
		{name: "external symlink", symlink: true},
		{name: "different regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "index.md")
			require.NoError(t, os.WriteFile(path, []byte("original archive"), 0o644))
			archiveRoot, err := os.OpenRoot(root)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, archiveRoot.Close()) })
			swapped := false
			opener := &diracSwapFileOpener{Root: archiveRoot, beforeOpen: func() {
				swapped = true
				require.NoError(t, os.Rename(path, path+"-prior"))
				if tc.symlink {
					outside := filepath.Join(t.TempDir(), "outside.md")
					require.NoError(t, os.WriteFile(outside, []byte("outside archive"), 0o644))
					if err := os.Symlink(outside, path); err != nil {
						t.Skipf("symlinks are unavailable: %v", err)
					}
					return
				}
				require.NoError(t, os.WriteFile(path, []byte("different archive"), 0o644))
			}}
			data, err := readDiracRegularFile(opener, "index.md", diracTaskIndexLimit)
			assert.True(t, swapped, "the test swaps the file after Lstat")
			require.Error(t, err, "the reader must refuse a file that changed after Lstat")
			assert.Empty(t, data)
		})
	}
}

func TestDiracArchiveUpdateKeepsTextToolAndResultSeparate(t *testing.T) {
	t.Parallel()
	events := []diracArchiveEvent{
		{Kind: "assistant_text", Details: json.RawMessage(`{"text":"first answer"}`)},
		{Kind: "tool_call", Details: json.RawMessage(`{"toolUseId":"read-1","name":"read_file","input":{"path":"note.txt"}}`)},
		{Kind: "tool_result", Details: json.RawMessage(`{"toolUseId":"read-1","result":"file contents"}`)},
		{Kind: "terminal", Details: json.RawMessage(`{"result":"done"}`)},
	}
	var updates []map[string]any
	for _, event := range events {
		raw, err := diracArchiveUpdate(event)
		require.NoError(t, err)
		var update map[string]any
		require.NoError(t, json.Unmarshal(raw, &update))
		updates = append(updates, update)
	}
	assert.Equal(t, []any{"agent_message_chunk", "tool_call", "tool_call_update", "agent_message_chunk"}, []any{
		updates[0]["sessionUpdate"], updates[1]["sessionUpdate"], updates[2]["sessionUpdate"], updates[3]["sessionUpdate"],
	})
	assert.Equal(t, "read-1", updates[1]["toolCallId"])
	assert.Equal(t, "read-1", updates[2]["toolCallId"])
	assert.Contains(t, string(mustDiracJSON(t, updates[2]["content"])), "file contents")
}

func mustDiracJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestReadDiracChildArchiveRejectsCollidingRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	for _, runID := range []string{"run-one", "run-two"} {
		writeDiracArchive(t, root, "my-task", runID, filepath.Join(runID, "transcript.md"), &child)
	}

	_, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	require.ErrorContains(t, err, "multiple dirac subagent runs")
}

func TestReadDiracChildArchiveRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	writeDiracArchive(t, root, "my-task", "run-one", "../../outside.md", &child)

	_, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	require.ErrorContains(t, err, "unsafe transcript path")
}

func TestReadDiracChildArchiveRejectsHeaderMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"}
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: diracArchiveSessionID, TS: 1}})
	writeDiracArchive(t, root, "my-task", "run-one", filepath.Join("run-one", "transcript.md"), &child)
	child.prompt = "Different task"

	_, err := readDiracChildArchive(root, diracArchiveSessionID, &child)
	require.ErrorContains(t, err, "identity differs")
}

func TestDiracArchiveWaitsForAFileAfterPromptEnd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	spawn := a.observeChildCard("call-delayed", "Ada", json.RawMessage(`{"isSubagent":true,"agentId":7,"agentName":"Ada","prompt":"Count files"}`), nil, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.observeChildCard("call-delayed", "Ada", nil, nil, "completed")
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)

	a.hydrateSubagentArchives(nil, false)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, row.Status, "an absent archive must not close the child route")

	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: "session-1", TS: 1}})
	writeDiracArchive(t, root, "my-task", "run-one", filepath.Join("run-one", "transcript.md"), &diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"})
	a.hydrateSubagentArchives(nil, false)
	row, ok = sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 5)
	assert.Contains(t, string(messages[1].Content), "ARCHIVED_CHILD_TEXT")
	assert.Contains(t, string(messages[4].Content), "ARCHIVED_CHILD_DONE")
}

func TestDiracArchiveKeepsItsRouteAndReplayPositionAfterAnInvalidEvent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	spawn := a.observeChildCard("call-partial", "Ada", json.RawMessage(`{"isSubagent":true,"agentId":7,"agentName":"Ada","prompt":"Count files"}`), nil, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.observeChildCard("call-partial", "Ada", nil, nil, "completed")
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: "session-1", TS: 1}})
	path := writeDiracArchive(t, root, "my-task", "run-one", filepath.Join("run-one", "transcript.md"), &diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"})
	complete, err := os.ReadFile(path)
	require.NoError(t, err)
	invalid := strings.Replace(string(complete), `"toolUseId": "read-1"`, `"toolUseId": ""`, 1)
	require.NotEqual(t, string(complete), invalid)
	require.NoError(t, os.WriteFile(path, []byte(invalid), 0o644))

	a.hydrateSubagentArchives(nil, false)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, row.Status, "an invalid later event must not close the child route")
	require.NoError(t, os.WriteFile(path, complete, 0o644))
	a.hydrateSubagentArchives(nil, false)
	row, ok = sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 5, "a retry must not duplicate the earlier archive text")
	var content strings.Builder
	for _, message := range messages {
		content.Write(message.Content)
	}
	assert.Equal(t, 1, strings.Count(content.String(), "ARCHIVED_CHILD_TEXT"))
	assert.Contains(t, string(messages[3].Content), "ARCHIVED_TOOL_RESULT")
	assert.Contains(t, string(messages[4].Content), "ARCHIVED_CHILD_DONE")
}
