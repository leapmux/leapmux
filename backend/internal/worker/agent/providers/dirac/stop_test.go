package dirac

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// writeDiracCheckpointStore lays out a minimal native archive for one session.
// runtime is the acp-session-runtime-configs record ("" writes none). taskID is
// the directory that conversation maps files into, under data/tasks/<taskID>/.
func writeDiracCheckpointStore(t *testing.T, root, sessionID, runtime, history, taskID string, conversation map[string]string) {
	t.Helper()
	files := map[string]string{
		"data/state/taskHistory.json": history,
	}
	if runtime != "" {
		files["data/acp-session-runtime-configs/"+sessionID+".json"] = runtime
	}
	for name, data := range conversation {
		files["data/tasks/"+taskID+"/"+name] = data
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	}
}

func runtimeConfig(taskID, mode string) string {
	return fmt.Sprintf(`{"version":1,"settings":{"mode":%q},"taskId":%q}`, mode, taskID)
}

const diracUserMessageHistory = `{"offset":0,"type":"append_message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"

func matchingHistory(taskID, sessionID string) string {
	return fmt.Sprintf(`[{"id":%q,"ulid":%q,"ts":1,"task":"T","cwdOnTaskInitialization":"/w"}]`, taskID, sessionID)
}

func duplicateDiracHistory(taskID, sessionID string) string {
	row := strings.Trim(matchingHistory(taskID, sessionID), "[]")
	return "[" + row + "," + row + "]"
}

func TestProbeDiracCheckpointConfirmsAUsableCheckpoint(t *testing.T) {
	t.Parallel()
	sessionID := "01SESSION"
	taskID := "native-root"
	cases := []struct {
		name         string
		runtime      string
		history      string
		conversation map[string]string
	}{
		{
			name:         "operations only",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.jsonl": diracUserMessageHistory},
		},
		{
			name:         "framed baseline",
			runtime:      runtimeConfig(taskID, "plan"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.baseline.jsonl": `{"type":"baseline","offset":-1}` + "\n" + `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"},
		},
		{
			name:         "legacy snapshot",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.json": `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		},
		{
			name:    "baseline with operations",
			runtime: runtimeConfig(taskID, "act"),
			history: matchingHistory(taskID, sessionID),
			conversation: map[string]string{
				"api_conversation_history.baseline.jsonl": `{"type":"baseline","offset":0}` + "\n" + `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n",
				"api_conversation_history.jsonl":          `{"offset":1,"type":"append_message","message":{"role":"assistant","content":[{"type":"text","text":"yo"}]}}` + "\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracCheckpointStore(t, root, sessionID, tc.runtime, tc.history, taskID, tc.conversation)
			probe := probeDiracCheckpoint(root, sessionID, sessionstore.OpenArchiveRoot)
			assert.True(t, probe.task, "a linked task is active work")
			assert.True(t, probe.ready, "a safe, stable task with a plan/act mode, one matching index row and readable conversation is a usable checkpoint")
		})
	}
}

func TestProbeDiracCheckpointRejectsAnUnusableCheckpoint(t *testing.T) {
	t.Parallel()
	sessionID := "01SESSION"
	taskID := "native-root"
	gapped := `{"offset":5,"type":"append_message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"
	readable := map[string]string{"api_conversation_history.jsonl": diracUserMessageHistory}
	cases := []struct {
		name         string
		runtime      string
		history      string
		conversation map[string]string
		wantTask     bool
	}{
		{
			name:     "no runtime link",
			runtime:  "",
			history:  matchingHistory(taskID, sessionID),
			wantTask: true,
		},
		{
			name:     "unsafe task id",
			runtime:  runtimeConfig("../escape", "act"),
			history:  `[]`,
			wantTask: false,
		},
		{
			name:         "runtime mode is not plan or act",
			runtime:      runtimeConfig(taskID, "edit"),
			history:      matchingHistory(taskID, sessionID),
			conversation: readable,
			wantTask:     true,
		},
		{
			name:         "missing runtime mode",
			runtime:      runtimeConfig(taskID, ""),
			history:      matchingHistory(taskID, sessionID),
			conversation: readable,
			wantTask:     true,
		},
		{
			name:         "no matching history row",
			runtime:      runtimeConfig(taskID, "act"),
			history:      `[]`,
			conversation: readable,
			wantTask:     true,
		},
		{
			name:         "history row owned by another session",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, "OTHER"),
			conversation: readable,
			wantTask:     true,
		},
		{
			name:         "duplicate task rows",
			runtime:      runtimeConfig(taskID, "act"),
			history:      duplicateDiracHistory(taskID, sessionID),
			conversation: readable,
			wantTask:     true,
		},
		{
			name:     "matching row with no conversation files",
			runtime:  runtimeConfig(taskID, "act"),
			history:  matchingHistory(taskID, sessionID),
			wantTask: true,
		},
		{
			name:         "offset gap",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.jsonl": gapped},
			wantTask:     true,
		},
		{
			name:         "incomplete first journal record",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.jsonl": `{"offset":0,"type":"append_message"`},
			wantTask:     true,
		},
		{
			name:         "empty operations yield no recoverable data",
			runtime:      runtimeConfig(taskID, "act"),
			history:      matchingHistory(taskID, sessionID),
			conversation: map[string]string{"api_conversation_history.jsonl": ""},
			wantTask:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracCheckpointStore(t, root, sessionID, tc.runtime, tc.history, taskID, tc.conversation)
			probe := probeDiracCheckpoint(root, sessionID, sessionstore.OpenArchiveRoot)
			assert.False(t, probe.ready, "an unusable archive must not confirm a checkpoint")
			assert.Equal(t, tc.wantTask, probe.task, "the task flag separates active work from an idle session")
		})
	}
}

func TestDiracHistoryMatchRequiresExactlyOneOwnedRow(t *testing.T) {
	t.Parallel()
	record := diracHistoryRecord{ID: "task-1", ULID: "S1"}
	assert.True(t, diracHistoryMatch([]diracHistoryRecord{record}, "task-1", "S1"))
	assert.False(t, diracHistoryMatch([]diracHistoryRecord{record}, "task-1", "S2"), "a row owned by another session is not a match")
	assert.False(t, diracHistoryMatch([]diracHistoryRecord{record, record}, "task-1", "S1"), "a duplicate task row is not a match")
	assert.False(t, diracHistoryMatch(nil, "task-1", "S1"), "no row is not a match")
	assert.False(t, diracHistoryMatch([]diracHistoryRecord{{ID: "other", ULID: "S1"}}, "task-1", "S1"))
}

func TestDecodeDiracRuntimeLinkReadsTheTaskAndMode(t *testing.T) {
	t.Parallel()
	link, err := decodeDiracRuntimeLink([]byte(`{"version":1,"settings":{"mode":"plan"},"taskId":"native-root"}`))
	require.NoError(t, err)
	assert.Equal(t, diracRuntimeLink{TaskID: "native-root", Mode: "plan"}, link)

	_, err = decodeDiracRuntimeLink([]byte(`{"settings":{"mode":"act"},"taskId":"../escape"}`))
	assert.Error(t, err, "an unsafe task id is not a valid runtime link")

	_, err = decodeDiracRuntimeLink([]byte(`not json`))
	assert.Error(t, err)

	_, err = decodeDiracRuntimeLink([]byte(`{"settings":{"mode":"act"}}`))
	assert.Error(t, err, "a record with no task id is not a valid runtime link")
}

// spycancel counts the cancellations a wait sends.
type spyCancel struct{ calls atomic.Int32 }

func (s *spyCancel) fn() { s.calls.Add(1) }

func TestWaitForDiracCheckpointReturnsImmediatelyForAConfirmedCheckpoint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), matchingHistory("native-root", "01SESSION"), "native-root", map[string]string{
		"api_conversation_history.jsonl": diracUserMessageHistory,
	})
	cancel := &spyCancel{}
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: context.Background(), clock: quartz.NewReal(),
	}
	done := make(chan struct{})
	go func() { waitForDiracCheckpoint(w); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a confirmed checkpoint must return without waiting for the budget")
	}
	assert.Zero(t, cancel.calls.Load(), "a confirmed checkpoint sends no cancellation")
}

func TestWaitForDiracCheckpointIgnoresAnIdleSession(t *testing.T) {
	t.Parallel()
	cancel := &spyCancel{}
	w := diracCheckpointWait{
		agentID: "test", profile: t.TempDir(), sessionID: "01SESSION",
		promptActive: false, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: context.Background(), clock: quartz.NewReal(),
	}
	done := make(chan struct{})
	go func() { waitForDiracCheckpoint(w); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an idle session must return without polling")
	}
	assert.Zero(t, cancel.calls.Load(), "an idle session sends no cancellation")
}

func TestWaitForDiracCheckpointCancelsOnceThenStopsOnProcessExit(t *testing.T) {
	t.Parallel()
	// A runtime link with no history index: active work that is not yet
	// checkpointed.
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	cancel := &spyCancel{}
	processDone := make(chan struct{})
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: processDone, ctx: context.Background(), clock: quartz.NewReal(),
	}
	done := make(chan struct{})
	go func() { waitForDiracCheckpoint(w); close(done) }()
	waitForCalls(t, func() bool { return cancel.calls.Load() == 1 })
	close(processDone)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the wait must return on process exit")
	}
	assert.Equal(t, int32(1), cancel.calls.Load(), "the wait queues at most one cancellation")
}

func TestWaitForDiracCheckpointStopsOnCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancel := &spyCancel{}
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: ctx, clock: quartz.NewReal(),
	}
	done := make(chan struct{})
	go func() { waitForDiracCheckpoint(w); close(done) }()
	waitForCalls(t, func() bool { return cancel.calls.Load() == 1 })
	cancelCtx()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the wait must return when its context ends")
	}
}

func TestWaitForDiracCheckpointGivesUpAtTheBudget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	cancel := &spyCancel{}
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: context.Background(), clock: quartz.NewReal(),
	}
	done := make(chan struct{})
	go func() { waitForDiracCheckpoint(w); close(done) }()
	waitForCalls(t, func() bool { return cancel.calls.Load() == 1 })
	// The archive never becomes ready, so the two-second budget ends the wait.
	select {
	case <-done:
	case <-time.After(2*diracStopBudget + time.Second):
		t.Fatal("the wait must return when the budget expires")
	}
	assert.Equal(t, int32(1), cancel.calls.Load())
}

// waitForCalls polls until condition holds, failing the test if it never does.
func waitForCalls(t *testing.T, condition func() bool) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("the expected side effect never arrived")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDiracCheckpointRejectsIncompleteFirstOperationWithSeed(t *testing.T) {
	t.Parallel()
	seeds := map[string]string{
		"api_conversation_history.baseline.jsonl": "{\"type\":\"baseline\",\"offset\":-1}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n",
		"api_conversation_history.json":           `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`,
	}
	for name, seed := range seeds {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracCheckpointStore(t, root, "session", runtimeConfig("task", "act"), matchingHistory("task", "session"), "task", map[string]string{
				name:                             seed,
				"api_conversation_history.jsonl": diracUserMessageHistory[:len(diracUserMessageHistory)-1],
			})
			probe := probeDiracCheckpoint(root, "session", sessionstore.OpenArchiveRoot)
			assert.True(t, probe.task)
			assert.False(t, probe.ready, "a seed cannot make an unfinished first operation safe")
		})
	}
}

type changingDiracCheckpointRoot struct {
	sessionstore.ArchiveRoot
	beforeOpen func(string)
}

func (r changingDiracCheckpointRoot) Open(path string) (*os.File, error) {
	r.beforeOpen(path)
	return r.ArchiveRoot.Open(path)
}

func (r changingDiracCheckpointRoot) OpenChild(path string) (sessionstore.ArchiveRoot, error) {
	child, err := r.ArchiveRoot.OpenChild(path)
	if err != nil {
		return nil, err
	}
	return changingDiracCheckpointRoot{ArchiveRoot: child, beforeOpen: r.beforeOpen}, nil
}

func TestDiracCheckpointRejectsHistoryReplacedDuringConversationRead(t *testing.T) {
	t.Parallel()
	profile := t.TempDir()
	writeDiracCheckpointStore(t, profile, "session", runtimeConfig("task", "act"), matchingHistory("task", "session"), "task", map[string]string{
		"api_conversation_history.jsonl": diracUserMessageHistory,
	})
	root, err := sessionstore.OpenArchiveRoot(profile)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	index := filepath.Join(profile, "data", "state", "taskHistory.json")
	var changed bool
	archive := changingDiracCheckpointRoot{ArchiveRoot: root, beforeOpen: func(path string) {
		if path == "api_conversation_history.jsonl" && !changed {
			changed = true
			require.NoError(t, os.Rename(index, index+".old"))
			require.NoError(t, os.WriteFile(index, []byte(matchingHistory("task", "different-session")), 0o600))
		}
	}}
	probe := evaluateDiracCheckpoint(archive, "session")
	assert.True(t, changed)
	assert.True(t, probe.task)
	assert.False(t, probe.ready, "the index and conversation must belong to one archive observation")
}

func TestDiracCheckpointReportsAReadThatCannotComplete(t *testing.T) {
	t.Parallel()
	probe := probeDiracCheckpoint(t.TempDir(), "session", func(string) (sessionstore.ArchiveRoot, error) {
		return nil, os.ErrPermission
	})
	assert.ErrorIs(t, probe.err, os.ErrPermission)
	assert.False(t, probe.ready)

	clock := testutil.NewQuartzMock(t)
	cancel := &spyCancel{}
	done := make(chan struct{})
	go func() {
		waitForDiracCheckpoint(diracCheckpointWait{
			profile: "profile", sessionID: "session", promptActive: true, cancel: cancel.fn,
			processDone: make(chan struct{}), ctx: t.Context(), clock: clock,
			openArchive: func(string) (sessionstore.ArchiveRoot, error) { return nil, os.ErrPermission },
		})
		close(done)
	}()
	awaitDiracTestStop(t, done)
	assert.Zero(t, cancel.calls.Load())
}
