package dirac

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

func probeDiracCheckpoint(profile, sessionID string, openArchive func(string) (sessionstore.ArchiveRoot, error)) diracCheckpointProbe {
	var observation diracCheckpointObservation
	return observation.probe(profile, sessionID, openArchive)
}

func evaluateDiracCheckpoint(root sessionstore.ArchiveRoot, sessionID string) diracCheckpointProbe {
	var observation diracCheckpointObservation
	return observation.evaluate(root, sessionID)
}

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
type spyCancel struct {
	calls  atomic.Int32
	called chan struct{}
}

func (s *spyCancel) fn() {
	if s.calls.Add(1) == 1 && s.called != nil {
		close(s.called)
	}
}

func startDiracCheckpointWait(wait diracCheckpointWait) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForDiracCheckpoint(wait)
	}()
	return done
}

func awaitDiracTestCancel(t *testing.T, cancel *spyCancel) {
	t.Helper()
	select {
	case <-cancel.called:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the checkpoint wait did not request cancellation")
	}
}

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
		processDone: make(chan struct{}), ctx: testutil.DeadlineContext(t), clock: testutil.NewQuartzMock(t),
	}
	done := startDiracCheckpointWait(w)
	awaitDiracTestStop(t, done)
	assert.Zero(t, cancel.calls.Load(), "a confirmed checkpoint sends no cancellation")
}

func TestWaitForDiracCheckpointIgnoresAnIdleSession(t *testing.T) {
	t.Parallel()
	cancel := &spyCancel{}
	w := diracCheckpointWait{
		agentID: "test", profile: t.TempDir(), sessionID: "01SESSION",
		promptActive: false, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: testutil.DeadlineContext(t), clock: testutil.NewQuartzMock(t),
	}
	done := startDiracCheckpointWait(w)
	awaitDiracTestStop(t, done)
	assert.Zero(t, cancel.calls.Load(), "an idle session sends no cancellation")
}

func TestWaitForDiracCheckpointCancelsOnceThenStopsOnProcessExit(t *testing.T) {
	t.Parallel()
	// A runtime link with no history index: active work that is not yet
	// checkpointed.
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	cancel := &spyCancel{called: make(chan struct{})}
	processDone := make(chan struct{})
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: processDone, ctx: testutil.DeadlineContext(t), clock: testutil.NewQuartzMock(t),
	}
	done := startDiracCheckpointWait(w)
	awaitDiracTestCancel(t, cancel)
	close(processDone)
	awaitDiracTestStop(t, done)
	assert.Equal(t, int32(1), cancel.calls.Load(), "the wait queues at most one cancellation")
}

func TestWaitForDiracCheckpointStopsOnCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	ctx, cancelCtx := context.WithCancel(testutil.DeadlineContext(t))
	t.Cleanup(cancelCtx)
	cancel := &spyCancel{called: make(chan struct{})}
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: ctx, clock: testutil.NewQuartzMock(t),
	}
	done := startDiracCheckpointWait(w)
	awaitDiracTestCancel(t, cancel)
	cancelCtx()
	awaitDiracTestStop(t, done)
}

func TestWaitForDiracCheckpointGivesUpAtTheBudget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracCheckpointStore(t, root, "01SESSION", runtimeConfig("native-root", "act"), `[]`, "native-root", nil)
	cancel := &spyCancel{called: make(chan struct{})}
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	poll := clock.Trap().NewTimer(diracStopPollTimerTag)
	t.Cleanup(poll.Close)
	w := diracCheckpointWait{
		agentID: "test", profile: root, sessionID: "01SESSION",
		promptActive: true, cancel: cancel.fn,
		processDone: make(chan struct{}), ctx: ctx, clock: clock,
	}
	done := startDiracCheckpointWait(w)
	awaitDiracTestCancel(t, cancel)
	// The archive never becomes ready. Advance through each scheduled poll
	// until the budget expires.
	for remaining := diracStopBudget; remaining > 0; remaining -= diracStopPollInterval {
		require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
		clock.Advance(min(remaining, diracStopPollInterval)).MustWait(ctx)
	}
	awaitDiracTestStop(t, done)
	assert.Equal(t, int32(1), cancel.calls.Load())
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

func TestDiracCheckpointDoesNotFallBackAfterTheBaselineDisappears(t *testing.T) {
	t.Parallel()

	const baseline = "api_conversation_history.baseline.jsonl"
	const legacy = "api_conversation_history.json"
	profile := t.TempDir()
	writeDiracCheckpointStore(t, profile, "session", runtimeConfig("task", "act"), matchingHistory("task", "session"), "task", map[string]string{
		baseline: "invalid baseline",
		legacy:   `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`,
	})
	path := filepath.Join(profile, "data", "tasks", "task", baseline)
	root, err := sessionstore.OpenArchiveRoot(profile)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	var removed, restored bool
	archive := changingDiracCheckpointRoot{ArchiveRoot: root, beforeOpen: func(name string) {
		switch name {
		case baseline:
			require.NoError(t, os.Rename(path, path+".away"))
			removed = true
		case legacy:
			require.NoError(t, os.Rename(path+".away", path))
			restored = true
		}
	}}
	t.Cleanup(func() {
		if removed && !restored {
			assert.NoError(t, os.Rename(path+".away", path))
		}
	})

	probe := evaluateDiracCheckpoint(archive, "session")
	assert.True(t, removed)
	assert.False(t, probe.ready, "a baseline that vanished during the probe cannot authorize a legacy fallback")
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

func TestWaitForDiracCheckpointReusesUnchangedArchiveObservation(t *testing.T) {
	t.Parallel()
	profile := t.TempDir()
	writeDiracCheckpointStore(t, profile, "session", runtimeConfig("task", "act"), matchingHistory("task", "session"), "task", map[string]string{
		"api_conversation_history.jsonl": strings.TrimSuffix(diracUserMessageHistory, "\n"),
	})
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	poll := clock.Trap().NewTimer(diracStopPollTimerTag)
	t.Cleanup(poll.Close)
	processDone := make(chan struct{})
	var historyReads, operationReads atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForDiracCheckpoint(diracCheckpointWait{
			profile: profile, sessionID: "session", promptActive: true,
			processDone: processDone, ctx: ctx, clock: clock,
			openArchive: func(path string) (sessionstore.ArchiveRoot, error) {
				root, err := sessionstore.OpenArchiveRoot(path)
				if err != nil {
					return nil, err
				}
				return changingDiracCheckpointRoot{ArchiveRoot: root, beforeOpen: func(name string) {
					switch name {
					case "taskHistory.json":
						historyReads.Add(1)
					case "api_conversation_history.jsonl":
						operationReads.Add(1)
					}
				}}, nil
			},
		})
	}()
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	clock.Advance(diracStopPollInterval).MustWait(ctx)
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	assert.Equal(t, int32(1), historyReads.Load(), "an unchanged index needs no new read")
	assert.Equal(t, int32(1), operationReads.Load(), "an unchanged incomplete operation needs no new read")
	close(processDone)
	awaitDiracTestStop(t, done)
}
