package dirac

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installDiracTestWatch(a *Agent, sessionID string) *diracTaskletWatch {
	watch := &diracTaskletWatch{sessionID: sessionID, observed: make(map[string]string), writers: make(map[string]*diracTaskletTranscript)}
	a.taskletWatch = watch
	return watch
}

func (a *Agent) pollDiracTasklets(watch *diracTaskletWatch) (bool, error) {
	result, err := a.pollDiracTaskletsPass(watch, false)
	return result.finished, err
}

func TestDiracTaskletSettlementRetainsAnUnfinishedNativeTool(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	path := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "api_conversation_history.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"role":"assistant","content":[{"type":"tool_use","id":"unfinished-read","name":"read_file","input":{"paths":["a.txt"]}}]}]`), 0o600))
	watch := installDiracTestWatch(a, "dirac-session")
	_, err := a.pollDiracTasklets(watch)
	require.NoError(t, err)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	var closing int
	for _, message := range sink.Child(rows[0].ChildAgentID).Messages() {
		if message.Closing {
			closing++
			assert.Equal(t, agent.MessageCompletionFinished, message.Completion, "a missing native result provides no tool outcome")
			var record map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(message.Content, &record))
			assert.NotContains(t, record, "result", "retention must not fabricate native tool output")
		}
	}
	assert.Equal(t, 1, closing)
}

func TestDiracTaskletResumeUsesItsExplicitNativeLifetime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	appendDiracTasklet(t, root, "Bea", "another-native-conversation")
	path := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Bea", "tasklet.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var sibling map[string]any
	require.NoError(t, json.Unmarshal(raw, &sibling))
	delete(sibling, "outcome")
	raw, err = json.Marshal(sibling)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	watch := installDiracTestWatch(a, "dirac-session")
	finished, err := a.pollDiracTasklets(watch)
	require.NoError(t, err)
	assert.False(t, finished, "the sibling keeps this watcher active")
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	key := diracTaskletKey("dirac-session", "Witten", "01M7P10WRK68MMQJW753C9KJ2T")
	first, exists := sink.BackgroundTask(key)
	require.True(t, exists)
	require.Equal(t, bgtask.StatusSucceeded, first.Status)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	_, err = a.pollDiracTasklets(watch)
	require.ErrorIs(t, err, agent.ErrChildRouteNotReady)
	beforeEvent, exists := sink.BackgroundTask(key)
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, beforeEvent.Status, "missing outcome alone does not identify a new native lifetime")
	events := `[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"native-resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Inspect the follow-up."}}]`
	path = filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
	require.NoError(t, os.WriteFile(path, []byte(events), 0o600))
	refused := errors.New("native resumed activity write refused")
	child := sink.Child(first.ChildAgentID)
	child.ReviveErr = refused
	_, err = a.pollDiracTasklets(watch)
	require.ErrorIs(t, err, refused)
	assert.Equal(t, "native-created", watch.writers[key].lifecycleID, "refused admission cannot claim the new native lifetime")
	child.ReviveErr = nil
	_, err = a.pollDiracTasklets(watch)
	require.NoError(t, err)
	resumed, exists := sink.BackgroundTask(key)
	require.True(t, exists)
	assert.Equal(t, first.ChildAgentID, resumed.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, resumed.Status)
	assert.Equal(t, "01M7P10WRK68MMQJW753C9KJ2T", sink.Child(resumed.ChildAgentID).LastSessionID())
}

func TestDiracTaskletWatchRequiresItsNonemptyExactRoot(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"", "another-root"} {
		t.Run(sessionID, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
			watch := installDiracTestWatch(a, "dirac-session")
			a.SetSessionIDForTest(sessionID)
			assert.False(t, a.diracTaskletWatchCurrent(watch))
		})
	}
}

func TestDiracTaskletHistoryRetryKeepsItsRouteAndImmutableRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	watch := installDiracTestWatch(a, "dirac-session")
	path := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "api_conversation_history.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"role":"user","content":[{"type":"text","text":"Inspect the parser"}]}]`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "ui_messages.json"), []byte(`[{"id":"native-text","conversationHistoryIndex":0,"content":{"type":"markdown","role":"assistant","content":"Previously saved native prefix"}}]`), 0o600))
	finished, err := a.pollDiracTasklets(watch)
	require.NoError(t, err)
	assert.False(t, finished)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	childID := rows[0].ChildAgentID
	previous := sink.Child(childID).Messages()
	require.Len(t, previous, 2, "the prompt and native text must already exist before recovery fails")
	mutateDiracTaskletRecord(t, root, func(record map[string]any) {
		record["outcome"] = map[string]any{"kind": "completed", "response": "The parser is correct.", "completedAt": 1}
	})
	require.NoError(t, os.WriteFile(path, []byte(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"Incomplete native history"}]}]`), 0o600))
	finished, err = a.pollDiracTasklets(watch)
	require.ErrorContains(t, err, "no owning declaration")
	assert.False(t, finished)
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
	assert.Equal(t, previous, sink.Child(childID).Messages(), "a failed native read must preserve the stored text prefix")
	require.NoError(t, os.WriteFile(path, []byte(`[{"role":"user","content":[{"type":"text","text":"Inspect the parser"}]},{"role":"assistant","content":[{"type":"tool_use","id":"read-1","name":"read_file","input":{"paths":["a.txt"]}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"Recovered native result"}]}]`), 0o600))
	finished, err = a.pollDiracTasklets(watch)
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	_, err = a.pollDiracTasklets(watch)
	require.NoError(t, err)
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, childID, rows[0].ChildAgentID)
	messages := sink.Child(childID).Messages()
	require.Len(t, messages, 4, "the prompt, saved native text and one native tool pair survive retry")
	assert.Equal(t, previous[1].Content, messages[1].Content, "recovery cannot replace the original native text bytes")
}

func TestDiracTaskletClearSeparatesReusedNativeIdentities(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	first := installDiracTestWatch(a, "dirac-session")
	_, err := a.pollDiracTasklets(first)
	require.NoError(t, err)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	firstID := rows[0].ChildAgentID
	next, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", next)
	assert.Equal(t, bgtask.StatusStopped, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.NoError(t, err)
	require.ErrorIs(t, a.publishDiracTasklet(first, metadata.Tasklets[0]), agent.ErrChildRouteNotReady)
	writeDiracTaskletStore(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "data", "acp-session-runtime-configs", next+".json"), []byte(`{"taskId":"native-root"}`), 0o600))
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { record["conversationUlid"] = "new-child-conversation" })
	second := installDiracTestWatch(a, next)
	_, err = a.pollDiracTasklets(second)
	require.NoError(t, err, "root=%q sink=%q rows=%+v children=%v", a.CurrentSessionID(), sink.LastSessionID(), sink.BackgroundTasks(), sink.ChildAgentIDs())
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 2)
	var secondID string
	for _, row := range rows {
		if row.ChildAgentID != firstID {
			secondID = row.ChildAgentID
		}
	}
	require.NotEmpty(t, secondID)
	assert.Equal(t, "new-child-conversation", sink.Child(secondID).LastSessionID())
}

func TestDiracSubagentIgnoresTheRemovedNumericCardProtocol(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	a.SetPromptActiveForTest(true)
	a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "not-a-native-tasklet", "name": "read_file", "kind": "read", "status": "in_progress", "rawInput": map[string]any{"isSubagent": true, "agentId": 7, "agentName": "Unrelated", "prompt": "A root tool argument"}}))
	require.Len(t, sink.Messages(), 1, "the current root actually processes the native tool")
	assert.Contains(t, string(sink.Messages()[0].Content), "not-a-native-tasklet")
	assert.Empty(t, sink.BackgroundTasks(), "only a current native spawn receipt can establish a tasklet")
}

func TestDiracTaskletReaderBacksOffWhileNativeFilesAreUnavailable(t *testing.T) {
	t.Parallel()
	a, _, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	a.SetSessionIDForTest("dirac-session")
	timer := clock.Trap().NewTimer(diracArchiveTimerTag)
	t.Cleanup(func() { timer.Close(); a.CancelForTest(); a.archiveWG.Wait() })
	a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))
	ctx := testutil.DeadlineContext(t)
	first := testutil.WaitForTimer(t, ctx, timer)
	assert.Equal(t, 250*time.Millisecond, first)
	clock.Advance(first).MustWait(ctx)
	second := testutil.WaitForTimer(t, ctx, timer)
	assert.Equal(t, 500*time.Millisecond, second)
}

func TestDiracAcceptedTaskletKeepsRunningPastTheRemovedArchiveDeadline(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, clock := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	timer := clock.Trap().NewTimer(diracArchiveTimerTag)
	t.Cleanup(func() { timer.Close(); a.CancelForTest(); a.archiveWG.Wait() })
	a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))
	ctx := testutil.DeadlineContext(t)
	for elapsed := time.Duration(0); elapsed < 3*time.Minute; {
		interval := testutil.WaitForTimer(t, ctx, timer)
		clock.Advance(interval).MustWait(ctx)
		elapsed += interval
	}
	testutil.WaitForTimer(t, ctx, timer)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status, "an accepted native operation supplies no child completion deadline")
	select {
	case status := <-sink.closed:
		t.Fatalf("the accepted child was closed without a native outcome: %v", status)
	default:
	}
}

func TestDiracTaskletStopAndClearCancelTheArchiveTimer(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"stop", "clear"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			a, sink, clock := newDiracSubagentAgentForRPC(t, root)
			a.SetSessionIDForTest("dirac-session")
			sink.UpdateSessionID("dirac-session")
			writeDiracTaskletStore(t, root)
			mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
			timer := clock.Trap().NewTimer(diracArchiveTimerTag)
			t.Cleanup(func() { timer.Close(); a.CancelForTest(); a.archiveWG.Wait() })
			a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))
			testutil.WaitForTimer(t, testutil.DeadlineContext(t), timer)
			if operation == "clear" {
				_, err := a.ClearContext()
				require.NoError(t, err)
			} else {
				a.CancelForTest()
			}
			a.archiveWG.Wait()
			_, pending := clock.Peek()
			assert.False(t, pending, "an outgoing native session must leave no scheduled archive read")
		})
	}
}

func TestDiracTaskletWatcherKeepsASpawnObservedDuringFinalPublication(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	sink.closeGate = gate
	t.Cleanup(func() { release(); a.CancelForTest(); a.archiveWG.Wait() })
	observed := make(chan struct{})
	previous := a.HooksForTest().SessionMetadataHandler
	a.HooksForTest().SessionMetadataHandler = func(updateType string, fields map[string]json.RawMessage, raw json.RawMessage) bool {
		result := previous(updateType, fields, raw)
		var header struct {
			ID string `json:"toolCallId"`
		}
		if json.Unmarshal(raw, &header) == nil && header.ID == "spawn-Bea" {
			close(observed)
		}
		return result
	}
	firstFrame := diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"})
	firstDone := make(chan struct{})
	go func() {
		a.HandleOutput(firstFrame)
		close(firstDone)
	}()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, ctx, sink))
	appendDiracTasklet(t, root, "Bea", "another-native-conversation")
	secondFrame := diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "spawn-Bea", "name": "subagents", "status": "completed"})
	secondDone := make(chan struct{})
	go func() {
		a.HandleOutput(secondFrame)
		close(secondDone)
	}()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("the second spawn did not reach its observer")
	}
	release()
	select {
	case <-firstDone:
	case <-ctx.Done():
		t.Fatal("the first spawn handler did not finish")
	}
	select {
	case <-secondDone:
	case <-ctx.Done():
		t.Fatal("the second spawn handler did not finish")
	}
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, ctx, sink))
	assert.Len(t, sink.BackgroundTasks(), 2)
}

func appendDiracTasklet(t *testing.T, root, id, conversation string) {
	t.Helper()
	title, prompt := "Second native child", "Inspect the second native assignment."
	cardID := "spawn-" + id
	row := map[string]any{"operation": "spawn", "taskletId": id, "title": title, "message": prompt, "outcome": "accepted"}
	card := map[string]any{"id": cardID, "toolName": "subagents", "status": "success", "taskletReceipt": map[string]any{"operation": "spawn", "outcome": "accepted", "rows": []any{row}}}
	operation := map[string]any{"offset": 1, "type": "create", "message": map[string]any{"id": cardID, "content": map[string]any{"type": "card", "card": card}, "conversationHistoryIndex": 2}}
	raw, err := json.Marshal(operation)
	require.NoError(t, err)
	path := filepath.Join(root, "data", "tasks", "native-root", "ui_messages.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, writeErr := file.Write(append(raw, '\n'))
	require.NoError(t, file.Close())
	require.NoError(t, writeErr)
	record := map[string]any{"id": id, "rootTaskId": "native-root", "conversationUlid": conversation, "title": title, "prompt": prompt, "createdAt": 0, "outcome": map[string]any{"kind": "completed", "response": "Second result", "completedAt": 1}}
	raw, err = json.Marshal(record)
	require.NoError(t, err)
	path = filepath.Join(root, "data", "tasks", "native-root", "tasklets", id, "tasklet.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	events := []any{
		map[string]any{"id": "accepted-" + id, "taskletId": id, "createdAt": 0, "communication": map[string]any{"type": "lifecycle", "state": "accepted"}},
		map[string]any{"id": "outcome-" + id, "taskletId": id, "createdAt": 1, "communication": map[string]any{"type": "outcome", "outcome": record["outcome"]}},
	}
	raw, err = json.Marshal(events)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "tasklet_events.json"), raw, 0o600))
}

func TestDiracTaskletExitPublishesItsCommittedOutcomeBeforeCleanup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, clock := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	eventsPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
	require.NoError(t, os.WriteFile(eventsPath, []byte(`[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}}]`), 0o600))
	timer, _ := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, diracArchiveRetryInterval, testutil.WaitForTimer(t, ctx, timer))
	mutateDiracTaskletRecord(t, root, func(record map[string]any) {
		record["outcome"] = map[string]any{"kind": "completed", "response": "Actual committed child result", "completedAt": 1}
	})
	require.NoError(t, os.WriteFile(eventsPath, []byte(`[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"native-outcome","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"Actual committed child result","completedAt":1}}}]`), 0o600))
	a.SimulateExitForTest()
	a.CancelForTest()
	require.NoError(t, a.Wait())
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status, "native completion precedes generic process-exit cleanup")
	child := sink.Child(rows[0].ChildAgentID)
	require.NotNil(t, child)
	notifications, err := json.Marshal(child.LeapMuxNotifications())
	require.NoError(t, err)
	assert.Contains(t, string(notifications), "Actual committed child result")
}

func TestDiracSettledResumedTaskletDoesNotSpuriouslyReviveOnWatcherRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, _ := newDiracSubagentAgentForRPC(t, root)
	a.SetSessionIDForTest("dirac-session")
	sink.UpdateSessionID("dirac-session")
	writeDiracTaskletStore(t, root)
	eventsPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
	resumedEvents := `[
		{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},
		{"id":"outcome:Witten:completed:1","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"Job 1 result.","completedAt":1}}},
		{"id":"native-resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Continue."}},
		{"id":"outcome:Witten:completed:2","taskletId":"Witten","createdAt":3,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"The parser is correct.","completedAt":1}}}
	]`
	require.NoError(t, os.WriteFile(eventsPath, []byte(resumedEvents), 0o600))

	first := installDiracTestWatch(a, "dirac-session")
	finished, err := a.pollDiracTasklets(first)
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))

	key := diracTaskletKey("dirac-session", "Witten", "01M7P10WRK68MMQJW753C9KJ2T")
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)
	child := sink.Child(row.ChildAgentID)
	require.NotNil(t, child)

	// Simulate a retired watcher restarting on the same settled resumed tasklet.
	child.ReviveErr = errors.New("unexpected revive during idle replay")
	second := installDiracTestWatch(a, "dirac-session")
	finished, err = a.pollDiracTasklets(second)
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	assert.Empty(t, sink.RevivedTasks(), "replaying an already-settled resumed tasklet must not revive its row")
	row, ok = sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Transition to a new resumed lifetime whose settled status differs from the prior closed state.
	// A history read failure must leave the prior closed row untouched instead of stranding it Running.
	mutateDiracTaskletRecord(t, root, func(record map[string]any) {
		record["outcome"] = map[string]any{
			"kind":     "failed",
			"failedAt": 4,
			"error":    map[string]any{"name": "Error", "message": "Job 3 failed"},
		}
	})
	failedEvents := `[
		{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},
		{"id":"native-resumed-2","taskletId":"Witten","createdAt":3,"communication":{"type":"lifecycle","state":"resumed","assignment":"Try again."}},
		{"id":"outcome:Witten:failed:4","taskletId":"Witten","createdAt":4,"communication":{"type":"outcome","outcome":{"kind":"failed","failedAt":4,"error":{"name":"Error","message":"Job 3 failed"}}}}
	]`
	require.NoError(t, os.WriteFile(eventsPath, []byte(failedEvents), 0o600))
	historyPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "api_conversation_history.json")
	require.NoError(t, os.WriteFile(historyPath, []byte(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"Broken history"}]}]`), 0o600))
	child.ReviveErr = nil
	third := installDiracTestWatch(a, "dirac-session")
	_, err = a.pollDiracTasklets(third)
	require.ErrorContains(t, err, "no owning declaration")
	assert.Empty(t, sink.RevivedTasks(), "a failed history read must not revive the row before settlement")
	row, ok = sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Once history is readable, the different settled status revives and closes together.
	require.NoError(t, os.WriteFile(historyPath, []byte(`[{"role":"user","content":[{"type":"text","text":"Inspect the parser"}]}]`), 0o600))
	finished, err = a.pollDiracTasklets(third)
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, bgtask.StatusFailed, waitDiracChildClose(t, testutil.DeadlineContext(t), sink))
	assert.Equal(t, []string{key}, sink.RevivedTasks())
	row, ok = sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestDiracTaskletOutcomeMismatchSettlesAfterGraceOrExit(t *testing.T) {
	t.Parallel()

	t.Run("after grace window", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		a, sink, clock := newDiracSubagentAgentForRPC(t, root)
		a.SetSessionIDForTest("dirac-session")
		sink.UpdateSessionID("dirac-session")
		writeDiracTaskletStore(t, root)
		eventsPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
		require.NoError(t, os.WriteFile(eventsPath, []byte(`[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}}]`), 0o600))

		timer := clock.Trap().NewTimer(diracArchiveTimerTag)
		t.Cleanup(func() { timer.Close(); a.CancelForTest(); a.archiveWG.Wait() })
		a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))

		ctx := testutil.DeadlineContext(t)
		first := testutil.WaitForTimer(t, ctx, timer)
		assert.Equal(t, diracArchiveRetryInterval, first)
		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusRunning, rows[0].Status, "mismatch defers settlement during the grace window")

		// Advance through backoff until the 5-second grace window elapses.
		clock.Advance(first).MustWait(ctx)
		second := testutil.WaitForTimer(t, ctx, timer)
		assert.Equal(t, 2*diracArchiveRetryInterval, second)
		elapsed := first + second
		clock.Advance(second).MustWait(ctx)
		for elapsed < 5*time.Second {
			interval := testutil.WaitForTimer(t, ctx, timer)
			clock.Advance(interval).MustWait(ctx)
			elapsed += interval
		}
		assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, ctx, sink))
		a.archiveWG.Wait()
		rows = sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
		child := sink.Child(rows[0].ChildAgentID)
		require.NotNil(t, child)
		notifications, err := json.Marshal(child.LeapMuxNotifications())
		require.NoError(t, err)
		assert.Contains(t, string(notifications), "The parser is correct.")
	})

	t.Run("on process exit during grace window", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		a, sink, clock := newDiracSubagentAgentForRPC(t, root)
		a.SetSessionIDForTest("dirac-session")
		sink.UpdateSessionID("dirac-session")
		writeDiracTaskletStore(t, root)
		eventsPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
		require.NoError(t, os.WriteFile(eventsPath, []byte(`[{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}}]`), 0o600))

		timer, _ := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
		a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))
		ctx := testutil.DeadlineContext(t)
		assert.Equal(t, diracArchiveRetryInterval, testutil.WaitForTimer(t, ctx, timer))

		a.SimulateExitForTest()
		a.CancelForTest()
		require.NoError(t, a.Wait())
		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status, "exit accepts tasklet.json outcome even when the journal lags")
	})
}

func TestDiracResumedTaskletWithStalePriorOutcomeStaysRunningPastGrace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		events string
	}{
		{
			name: "matching prior outcome before resume",
			events: `[
				{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},
				{"id":"outcome:Witten:completed:1","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"The parser is correct.","completedAt":1}}},
				{"id":"native-resumed","taskletId":"Witten","createdAt":1,"communication":{"type":"lifecycle","state":"resumed","assignment":"Inspect the follow-up."}}
			]`,
		},
		{
			name: "outcome timestamp older than resume without prior outcome event",
			events: `[
				{"id":"native-created","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},
				{"id":"native-resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Inspect the follow-up."}}
			]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			a, sink, clock := newDiracSubagentAgentForRPC(t, root)
			a.SetSessionIDForTest("dirac-session")
			sink.UpdateSessionID("dirac-session")
			writeDiracTaskletStore(t, root)
			eventsPath := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json")
			require.NoError(t, os.WriteFile(eventsPath, []byte(tc.events), 0o600))

			timer := clock.Trap().NewTimer(diracArchiveTimerTag)
			t.Cleanup(func() { timer.Close(); a.CancelForTest(); a.archiveWG.Wait() })
			a.HandleOutput(diracFrame(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-spawn-card", "name": "subagents", "status": "completed"}))

			ctx := testutil.DeadlineContext(t)
			for elapsed := time.Duration(0); elapsed < 15*time.Second; {
				interval := testutil.WaitForTimer(t, ctx, timer)
				clock.Advance(interval).MustWait(ctx)
				elapsed += interval
			}
			pendingInterval := testutil.WaitForTimer(t, ctx, timer)

			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, bgtask.StatusRunning, rows[0].Status, "a stale Job 1 outcome in tasklet.json must not force-close a running Job 2")
			select {
			case status := <-sink.closed:
				t.Fatalf("running Job 2 was closed with stale Job 1 outcome: %v", status)
			default:
			}

			// A real torn write for Job 2 (timestamp >= resumed.createdAt) still settles after the 5-second grace window.
			mutateDiracTaskletRecord(t, root, func(record map[string]any) {
				record["outcome"] = map[string]any{"kind": "completed", "response": "Job 2 result.", "completedAt": 5}
			})
			clock.Advance(pendingInterval).MustWait(ctx)
			for elapsed := time.Duration(0); elapsed < diracOutcomeMismatchGrace; {
				interval := testutil.WaitForTimer(t, ctx, timer)
				clock.Advance(interval).MustWait(ctx)
				elapsed += interval
			}
			assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, ctx, sink))
			a.archiveWG.Wait()
			rows = sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
			child := sink.Child(rows[0].ChildAgentID)
			require.NotNil(t, child)
			notifications, err := json.Marshal(child.LeapMuxNotifications())
			require.NoError(t, err)
			assert.Contains(t, string(notifications), "Job 2 result.")
		})
	}
}
