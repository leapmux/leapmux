package codewhale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const testChildID = "agent_34dcb3be"

var agentStartInput = map[string]any{"action": "start", "prompt": "Count the files.\nThen report.", "type": "explore", "name": "counter"}

// transcriptLine is one record of a child transcript file.
func transcriptLine(t *testing.T, index int, role string, blocks ...map[string]any) string {
	t.Helper()
	return string(mustJSON(t, map[string]any{"kind": "message", "index": index, "message": map[string]any{"role": role, "content": blocks}})) + "\n"
}

// appendTranscript appends to the child's transcript file, creating it.
func appendTranscript(t *testing.T, path, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// startChildRun spawns one child through the `agent` tool, as the stream states it.
func startChildRun(a *Agent) {
	a.HandleOutput(toolStartEvent(10, "item_spawn", "call_spawn", contracts.CodewhaleToolAgent, agentStartInput))
	a.HandleOutput(toolEndEvent(11, "item.completed", "item_spawn", "call_spawn", contracts.CodewhaleToolAgent,
		`{"name":"counter","agent_id":"`+testChildID+`","status":"running"}`, agentStartInput, map[string]any{"action": "start", "agent_id": testChildID}))
}

func TestChildTranscriptPath(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte(testChildID))
	assert.Equal(t, filepath.Join("/w", ".codewhale", "state", "subagent-transcripts", hex.EncodeToString(sum[:])+".jsonl"), childTranscriptPath("/w", testChildID))
}

func TestToolSpawnsSubagent(t *testing.T) {
	t.Parallel()
	assert.True(t, toolSpawnsSubagent(contracts.CodewhaleToolAgent, []byte(`{"action":"start","prompt":"x"}`)))
	assert.False(t, toolSpawnsSubagent(contracts.CodewhaleToolAgent, []byte(`{"action":"wait"}`)))
	assert.False(t, toolSpawnsSubagent(contracts.CodewhaleToolAgent, []byte(`not json`)))
	assert.False(t, toolSpawnsSubagent(contracts.CodewhaleToolBash, []byte(`{"action":"start"}`)))
}

func TestSubagentTitle(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "counter", subagentTitle(agentToolInput{Name: " counter ", Type: "explore", Prompt: "x"}))
	assert.Equal(t, "explore", subagentTitle(agentToolInput{Type: "explore", Prompt: "x"}))
	assert.Equal(t, "Count the files.", subagentTitle(agentToolInput{Prompt: "Count the files.\nThen report."}))
	assert.Empty(t, subagentTitle(agentToolInput{}))
}

func TestAgentRunStatus(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]bgtask.Status{
		"completed":   bgtask.StatusCompleted,
		"failed":      bgtask.StatusFailed,
		"cancelled":   bgtask.StatusStopped,
		"interrupted": bgtask.StatusStopped,
	} {
		status, final := agentRunStatus(word)
		assert.True(t, final, word)
		assert.Equal(t, want, status, word)
	}
	// Every other word of the ledger is a child that still works. A child parked
	// for a followup is one of them, and so is a word from a later release.
	for _, word := range []string{"queued", "starting", "running", "waiting_for_user", "model_wait", "running_tool", "a_later_word", ""} {
		status, final := agentRunStatus(word)
		assert.False(t, final, word)
		assert.Equal(t, bgtask.StatusRunning, status, word)
	}
}

func TestASubagentIsFollowedToItsEnd(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	var finished atomic.Bool
	rt.handle(http.MethodGet, routeAgentRuns+"/"+testChildID, func(w http.ResponseWriter, _ *http.Request) {
		if finished.Load() {
			writeFakeJSON(w, http.StatusOK, map[string]any{"status": "completed", "result_summary": "There are 3 files."})
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"status": "running"})
	})
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, rt, clock)
	// The trap closes BEFORE the watchers stop, because cleanups run in reverse
	// order. A watcher parked in a trapped call then returns from it, and the
	// stop that waits for the watcher does not wait for ever.
	tail := clock.Trap().NewTimer(childTimerTag)
	t.Cleanup(tail.Close)
	ctx := testutil.DeadlineContext(t)
	path := childTranscriptPath(a.workingDir, testChildID)
	appendTranscript(t, path, `{"kind":"subagent_transcript_header","schema_version":1,"agent_id":"`+testChildID+"\"}\n"+
		transcriptLine(t, 0, "user", map[string]any{"type": "text", "text": "Assignment metadata: ..."})+
		transcriptLine(t, 1, "assistant", map[string]any{"type": "thinking", "thinking": "Child thinking."}, map[string]any{"type": "tool_use", "id": "call_child", "name": "bash", "input": map[string]any{"command": "ls"}})+
		`{"kind":"message","index":2,"mess`)

	startChildRun(a)

	childID := "child-of-call_spawn"
	task, ok := sink.BackgroundTask(testChildID)
	require.True(t, ok)
	assert.Equal(t, childID, task.ChildAgentID)
	assert.Equal(t, bgtask.KindSubagent, task.Kind)
	assert.Equal(t, "counter", task.Title)
	assert.Equal(t, "Count the files.", task.Description)
	child := sink.Child(childID)
	assert.Contains(t, string(child.Messages()[0].Content), "Count the files.", "the child transcript opens on its prompt")

	// The first tick reads the file and polls the run, then arms the tail timer.
	// The watcher does nothing more until that timer fires or a wait wakes it.
	assert.Equal(t, childTailInterval, testutil.WaitForTimer(t, ctx, tail))
	rows := child.Messages()
	require.Len(t, rows, 3, "the prompt, then one row for each block; the partial record waits")
	assert.Contains(t, string(rows[1].Content), "Child thinking.")
	assert.Equal(t, "call_child", rows[2].SpanID)
	assert.Equal(t, "bash", rows[2].SpanType)

	// The runtime completes the record and writes more.
	appendTranscript(t, path, `age":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_child","content":"a\nb\nc"}]}}`+"\n"+
		transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "A replayed record."})+
		transcriptLine(t, 3, "user", map[string]any{"type": "text", "text": "Also count the directories."})+
		transcriptLine(t, 4, "assistant", map[string]any{"type": "text", "text": "There are 3 files."}))
	clock.Advance(childTailInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, tail)
	rows = child.Messages()
	require.Len(t, rows, 6, "the replayed index is skipped")
	assert.Equal(t, "call_child", rows[3].SpanID)
	assert.True(t, rows[3].Closing)
	assert.Equal(t, "bash", rows[3].SpanType)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[4].Source, "a later message to the child is the reader's own row")
	assert.Contains(t, string(rows[5].Content), "There are 3 files.")
	assert.Len(t, rt.requestsTo(http.MethodGet, routeAgentRuns+"/"+testChildID), 1, "a tail tick that is not a poll tick reads no status")

	// A wait that settled the child wakes the watcher at once: the clock does
	// not move, and the run status is read on the wake.
	finished.Store(true)
	a.HandleOutput(toolStartEvent(20, "item_wait", "call_wait", contracts.CodewhaleToolAgent, map[string]any{"action": "wait"}))
	a.HandleOutput(toolEndEvent(21, "item.completed", "item_wait", "call_wait", contracts.CodewhaleToolAgent,
		`{"action":"wait","settled":[{"agent_id":"`+testChildID+`","name":"counter","status":"completed"}],"running":0}`, map[string]any{"action": "wait"}, nil))
	require.Eventually(t, func() bool {
		a.children.mu.Lock()
		defer a.children.mu.Unlock()
		return len(a.children.byID) == 0
	}, 30*time.Second, 5*time.Millisecond, "the watcher finishes the child")
	task, _ = sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, sink.BackgroundTaskStatuses(testChildID))
	var reports []string
	for _, notification := range sink.LeapMuxNotifications() {
		reports = append(reports, string(mustJSON(t, notification)))
	}
	require.Len(t, reports, 1, "the child's summary is reported into the parent once")
	assert.Contains(t, reports[0], "There are 3 files.")
	assert.Contains(t, reports[0], "counter")
}

func TestAChildTheLedgerNeverKnewFails(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgentWithClock(t, rt, quartz.NewReal())
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", path: filepath.Join(t.TempDir(), "absent.jsonl"), nudge: make(chan struct{}, 1), openTools: map[string]string{"call_open": "bash"}}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: testChildID, Kind: bgtask.KindSubagent, ChildAgentID: "child-1", Status: bgtask.StatusRunning}))
	childSink := sink.ChildSink("child-1")
	ctx := testutil.DeadlineContext(t)
	for i := 1; i < childMissingLimit; i++ {
		assert.False(t, a.pollChild(ctx, childSink, child), i)
	}
	assert.True(t, a.pollChild(ctx, childSink, child))
	task, ok := sink.BackgroundTask(testChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, task.Status)
	assert.Contains(t, sink.Child("child-1").ClosedSpans(), "call_open", "a call the transcript never answered is closed")
	assert.Empty(t, sink.LeapMuxNotifications(), "a child with no summary reports nothing into the parent")
}

// Only a 404 counts toward the limit: a status read that fails for another
// reason establishes nothing, and a read that finds the run starts the count
// again.
func TestAChildWhoseRunCannotBeReadIsNotCountedMissing(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	rt.handle(http.MethodGet, routeAgentRuns+"/"+testChildID, func(w http.ResponseWriter, _ *http.Request) {
		code := int(status.Load())
		if code != http.StatusOK {
			writeFakeJSON(w, code, map[string]any{"error": map[string]any{"message": "no", "status": code}})
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"status": "running"})
	})
	a, sink := newTestAgent(t, rt)
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", path: filepath.Join(t.TempDir(), "absent.jsonl"), nudge: make(chan struct{}, 1), openTools: map[string]string{}}
	childSink := sink.ChildSink("child-1")
	ctx := testutil.DeadlineContext(t)

	for i := range childMissingLimit + 1 {
		assert.False(t, a.pollChild(ctx, childSink, child), i)
	}
	assert.Zero(t, child.missing, "a failed read is not a missing run")

	status.Store(http.StatusNotFound)
	for i := 1; i < childMissingLimit; i++ {
		assert.False(t, a.pollChild(ctx, childSink, child), i)
	}
	status.Store(http.StatusOK)
	assert.False(t, a.pollChild(ctx, childSink, child), "a running child is not finished")
	assert.Zero(t, child.missing, "a read that finds the run starts the count again")
	assert.Empty(t, sink.BackgroundTaskStatuses(testChildID), "no row closed")
}

// The runtime states a started child's id in the call's result text, in its
// metadata, or in both. Either one starts the child.
func TestAChildStartReadsItsIDFromTheResultOrTheMetadata(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		detail   string
		metadata map[string]any
		id       string
	}{
		"the result":   {`{"agent_id":"agent_a","status":"running"}`, nil, "agent_a"},
		"the metadata": {"Started.", map[string]any{"action": "start", "agent_id": "agent_b"}, "agent_b"},
	} {
		rt := newFakeRuntime(t)
		rt.respondJSON(http.MethodGet, routeAgentRuns+"/"+tc.id, http.StatusOK, map[string]any{"status": "running"})
		a, sink := newTestAgent(t, rt)
		a.HandleOutput(toolStartEvent(10, "item_spawn", "call_spawn", contracts.CodewhaleToolAgent, agentStartInput))
		a.HandleOutput(toolEndEvent(11, "item.completed", "item_spawn", "call_spawn", contracts.CodewhaleToolAgent, tc.detail, agentStartInput, tc.metadata))
		task, ok := sink.BackgroundTask(tc.id)
		require.True(t, ok, name)
		assert.Equal(t, bgtask.StatusRunning, task.Status, name)
		assert.Len(t, sink.BackgroundTasks(), 1, name)
	}
}

// The registry watches each child once, even when two results state the same
// child at the same time, and it starts nothing after a stop.
func TestCodewhaleChildrenWatchEachChildOnce(t *testing.T) {
	t.Parallel()
	children := newCodewhaleChildren(context.Background())
	var watchers atomic.Int32
	watch := func(ctx context.Context, _ *codewhaleChild) {
		watchers.Add(1)
		<-ctx.Done()
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if children.start(&codewhaleChild{agentID: testChildID, nudge: make(chan struct{}, 1)}, watch) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), accepted.Load())

	// A nudge never blocks, whether the child's channel is full or no child has
	// that id.
	children.wake(testChildID)
	children.wake(testChildID)
	children.wake("agent_unknown")

	children.stopAll()
	assert.Equal(t, int32(1), watchers.Load())
	assert.False(t, children.start(&codewhaleChild{agentID: "agent_late", nudge: make(chan struct{}, 1)}, watch), "nothing starts after a stop")
	assert.False(t, children.run(func(context.Context) { t.Error("a poller ran after a stop") }))
	children.stopAll()

	drained := children.drain()
	require.Len(t, drained, 1, "the stop leaves the child for the exit to close")
	assert.Equal(t, testChildID, drained[0].agentID)
	assert.Empty(t, children.drain())

	var none *codewhaleChildren
	none.stopAll()
	assert.Nil(t, none.drain())
}

// The reader drops a record longer than the line limit rather than hold it
// without limit, and it goes on with the record after it.
func TestAnOverlongTranscriptRecordIsDropped(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	path := filepath.Join(t.TempDir(), "child.jsonl")
	overlong := childMaxLine + childReadChunk
	appendTranscript(t, path, strings.Repeat("x", overlong))
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", path: path, nextIndex: 1, openTools: map[string]string{}}
	childSink := sink.ChildSink("child-1")

	for i := 0; i < 10 && child.offset < int64(overlong); i++ {
		a.tailChildTranscript(childSink, child)
		assert.LessOrEqual(t, len(child.pending), childMaxLine, "the held part never passes the limit")
	}
	require.Equal(t, int64(overlong), child.offset)
	assert.Empty(t, child.pending, "the reader dropped the record")

	// The runtime ends the record and writes the next one.
	appendTranscript(t, path, "the tail of the long record\n"+transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "After."}))
	a.tailChildTranscript(childSink, child)
	rows := sink.Child("child-1").Messages()
	require.Len(t, rows, 1, "the tail of the dropped record is not a record")
	assert.Contains(t, string(rows[0].Content), "After.")
}

func TestAFailedStartRunsNoChild(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(toolStartEvent(1, "item_1", "call_spawn", contracts.CodewhaleToolAgent, agentStartInput))
	a.HandleOutput(toolEndEvent(2, "item.failed", "item_1", "call_spawn", contracts.CodewhaleToolAgent, "spawn depth exceeded", agentStartInput, nil))
	assert.Empty(t, sink.BackgroundTasks())

	// A start whose result names no child starts nothing either.
	a.HandleOutput(toolStartEvent(3, "item_2", "call_spawn2", contracts.CodewhaleToolAgent, agentStartInput))
	a.HandleOutput(toolEndEvent(4, "item.completed", "item_2", "call_spawn2", contracts.CodewhaleToolAgent, "started", agentStartInput, nil))
	assert.Empty(t, sink.BackgroundTasks())
}

func TestAStopClosesTheChildrenItCutOff(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodGet, routeAgentRuns+"/"+testChildID, http.StatusOK, map[string]any{"status": "running"})
	a, sink := newTestAgent(t, rt)
	startChildRun(a)

	a.Stop()
	task, ok := sink.BackgroundTask(testChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, task.Status)
}

func TestTheTranscriptReaderRefusesAnythingButItsOwnFile(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	require.NoError(t, os.WriteFile(target, []byte(transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "Leaked."})), 0o600))
	link := filepath.Join(dir, "link.jsonl")
	require.NoError(t, os.Symlink(target, link))
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", path: link, openTools: map[string]string{}}
	a.tailChildTranscript(sink.ChildSink("child-1"), child)
	assert.Empty(t, sink.Child("child-1").Messages(), "a link is never followed")

	child.path = dir
	a.tailChildTranscript(sink.ChildSink("child-1"), child)
	assert.Empty(t, sink.Child("child-1").Messages(), "a directory is not a transcript")
}

func TestTheTranscriptReaderStartsOverOnAReplacedFile(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	path := filepath.Join(t.TempDir(), "child.jsonl")
	appendTranscript(t, path, transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "One."})+transcriptLine(t, 2, "assistant", map[string]any{"type": "text", "text": "Two."}))
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", path: path, nextIndex: 1, openTools: map[string]string{}}
	childSink := sink.ChildSink("child-1")
	a.tailChildTranscript(childSink, child)
	require.Len(t, sink.Child("child-1").Messages(), 2)

	// The runtime rewrote the file shorter: the reader rereads it, and the indexes
	// keep it from persisting a record twice.
	require.NoError(t, os.WriteFile(path, []byte(transcriptLine(t, 3, "assistant", map[string]any{"type": "text", "text": "Three."})), 0o600))
	a.tailChildTranscript(childSink, child)
	rows := sink.Child("child-1").Messages()
	require.Len(t, rows, 3)
	assert.Contains(t, string(rows[2].Content), "Three.")

	// A record that is not a message, or not JSON, persists nothing.
	appendTranscript(t, path, "not json\n"+`{"kind":"a_later_kind","index":9}`+"\n"+`{"kind":"message"}`+"\n")
	a.tailChildTranscript(childSink, child)
	assert.Len(t, sink.Child("child-1").Messages(), 3)
}

func TestAWorkflowRunIsARegistryRow(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	input := map[string]any{"action": "start", "name": "review", "plan": map[string]any{"goal": "Review the diff"}}
	for i, status := range []string{"running", "degraded"} {
		seq := uint64(10 * (i + 1))
		a.HandleOutput(toolStartEvent(seq, "item_w", "call_w", contracts.CodewhaleToolWorkflow, input))
		a.HandleOutput(toolEndEvent(seq+1, "item.completed", "item_w", "call_w", contracts.CodewhaleToolWorkflow, "ok", input, map[string]any{"run_id": "run-1", "status": status, "terminal": status != "running"}))
	}
	task, ok := sink.BackgroundTask("run-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, task.Kind)
	assert.Equal(t, "Review the diff", task.Title)
	assert.Equal(t, "run-1", task.GroupKey)
	assert.Equal(t, bgtask.StatusFailed, task.Status, "a degraded run failed some of its stages")

	// A call that states no run, and a failed call, write no row.
	a.HandleOutput(toolStartEvent(30, "item_x", "call_x", contracts.CodewhaleToolWorkflow, input))
	a.HandleOutput(toolEndEvent(31, "item.failed", "item_x", "call_x", contracts.CodewhaleToolWorkflow, "boom", input, map[string]any{"run_id": "run-2"}))
	_, ok = sink.BackgroundTask("run-2")
	assert.False(t, ok)
}

func TestAWorkflowRunTakesItsTitleAndItsEnd(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	input := map[string]any{"action": "status", "name": "review"}
	a.HandleOutput(toolStartEvent(1, "item_w", "call_w", contracts.CodewhaleToolWorkflow, input))
	a.HandleOutput(toolEndEvent(2, "item.completed", "item_w", "call_w", contracts.CodewhaleToolWorkflow, "ok", input, map[string]any{"run_id": "run-1", "status": "a_later_word", "terminal": true}))
	task, ok := sink.BackgroundTask("run-1")
	require.True(t, ok)
	assert.Equal(t, "review", task.Title, "a run with no goal takes the workflow's name")
	assert.Equal(t, bgtask.StatusCompleted, task.Status, "a run that the runtime marks as ended is complete, whatever word states its status")

	// A result that states no run writes no row.
	a.HandleOutput(toolStartEvent(3, "item_x", "call_x", contracts.CodewhaleToolWorkflow, input))
	a.HandleOutput(toolEndEvent(4, "item.completed", "item_x", "call_x", contracts.CodewhaleToolWorkflow, "ok", input, map[string]any{"status": "running"}))
	assert.Len(t, sink.BackgroundTasks(), 1)
}

func TestWorkflowRunStatus(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]bgtask.Status{
		contracts.CodewhaleWorkflowStatusCompleted: bgtask.StatusCompleted,
		contracts.CodewhaleWorkflowStatusDegraded:  bgtask.StatusFailed,
		contracts.CodewhaleWorkflowStatusFailed:    bgtask.StatusFailed,
		contracts.CodewhaleWorkflowStatusCancelled: bgtask.StatusStopped,
		contracts.CodewhaleWorkflowStatusRunning:   bgtask.StatusRunning,
		"a_later_word":                             bgtask.StatusRunning,
		"":                                         bgtask.StatusRunning,
	} {
		assert.Equal(t, want, workflowRunStatus(word), word)
	}
}

// A subagent's rows reach its own transcript and never the parent's.
func TestChildRowsStayInTheChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	child := &codewhaleChild{agentID: testChildID, childID: "child-1", nextIndex: 1, openTools: map[string]string{}}
	a.persistChildRecord(sink.ChildSink("child-1"), child, []byte(transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "Mine."})))
	assert.Empty(t, sink.Messages())
	assert.Len(t, sink.Child("child-1").Messages(), 1)
}

// --- background shells ---

const testShellTask = "shell_e2131998"

var shellLaunchArgs = map[string]any{"command": "sleep 2; echo bg-done"}

// shellLaunchResult is the final event of a `task_shell_start` call that started
// a job, as 0.9.13 and 0.10.0 both send it.
func shellLaunchResult(seq uint64, callID string) []byte {
	return toolEndEvent(seq, "item.completed", "item_"+callID, callID, contracts.CodewhaleToolTaskShellStart,
		"Background task started: "+testShellTask, shellLaunchArgs,
		map[string]any{"task_id": testShellTask, "backgrounded": true, "status": "Running", "exit_code": nil})
}

// launchShellJob runs one `task_shell_start` call through the stream.
func launchShellJob(a *Agent, seq uint64, callID string) {
	a.HandleOutput(toolStartEvent(seq, "item_"+callID, callID, contracts.CodewhaleToolTaskShellStart, shellLaunchArgs))
	a.HandleOutput(shellLaunchResult(seq+1, callID))
}

func TestShellJobStatus(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]bgtask.Status{
		"Completed": bgtask.StatusCompleted,
		"Failed":    bgtask.StatusFailed,
		"TimedOut":  bgtask.StatusFailed,
		"Killed":    bgtask.StatusStopped,
	} {
		status, final := shellJobStatus(word)
		assert.True(t, final, word)
		assert.Equal(t, want, status, word)
	}
	for _, word := range []string{"Running", "completed", "a_later_word", ""} {
		_, final := shellJobStatus(word)
		assert.False(t, final, word)
	}
}

// The model waits for the job, and the wait's result states that it ended.
func TestAShellJobIsARegistryRowUntilAWaitReportsItsEnd(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.Mu.Lock()
	a.shells.jobsRoutes = jobsRoutesMissing // 0.9.13: no poller
	a.Mu.Unlock()
	launchShellJob(a, 1, "call_start")

	task, ok := sink.BackgroundTask("call_start")
	require.True(t, ok, "the row key is the launching call's span")
	assert.Equal(t, bgtask.KindShell, task.Kind)
	assert.Equal(t, "sleep 2; echo bg-done", task.Title)
	assert.True(t, task.TitleIsCommand)
	assert.Equal(t, bgtask.StatusRunning, task.Status)

	// A wait on the job while it runs reports it running and backgrounded again,
	// and opens no second row.
	waitInput := map[string]any{"task_id": testShellTask}
	a.HandleOutput(toolStartEvent(3, "item_w1", "call_wait1", contracts.CodewhaleToolTaskShellWait, waitInput))
	a.HandleOutput(toolEndEvent(4, "item.completed", "item_w1", "call_wait1", contracts.CodewhaleToolTaskShellWait, "Output so far:\nbg", waitInput,
		map[string]any{"task_id": testShellTask, "backgrounded": true, "status": "Running"}))
	assert.Len(t, sink.BackgroundTasks(), 1)

	a.HandleOutput(toolStartEvent(5, "item_w2", "call_wait2", contracts.CodewhaleToolTaskShellWait, waitInput))
	a.HandleOutput(toolEndEvent(6, "item.completed", "item_w2", "call_wait2", contracts.CodewhaleToolTaskShellWait, "bg-done", waitInput,
		map[string]any{"task_id": testShellTask, "backgrounded": false, "status": "Completed", "exit_code": 0}))
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, sink.BackgroundTaskStatuses("call_start"))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.shells.rowKeys)
}

func TestAShellCallThatStartsNoJobOpensNoRow(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	// The deferred first call loads the schema and states no job.
	a.HandleOutput(toolStartEvent(1, "item_1", "call_load", contracts.CodewhaleToolTaskShellStart, shellLaunchArgs))
	a.HandleOutput(toolEndEvent(2, "item.completed", "item_1", "call_load", contracts.CodewhaleToolTaskShellStart, "Tool `task_shell_start` was deferred", shellLaunchArgs, map[string]any{"deferred_tool_loaded": true}))
	// A foreground command states a job id and a final status, and no job runs.
	a.HandleOutput(toolStartEvent(3, "item_2", "call_ls", contracts.CodewhaleToolBash, map[string]any{"command": "ls"}))
	a.HandleOutput(toolEndEvent(4, "item.completed", "item_2", "call_ls", contracts.CodewhaleToolBash, "a.ts", map[string]any{"command": "ls"}, map[string]any{"task_id": "shell_x", "backgrounded": false, "status": "Completed", "exit_code": 0}))
	// A failed launch states nothing that runs.
	a.HandleOutput(toolStartEvent(5, "item_3", "call_bad", contracts.CodewhaleToolTaskShellStart, shellLaunchArgs))
	a.HandleOutput(toolEndEvent(6, "item.failed", "item_3", "call_bad", contracts.CodewhaleToolTaskShellStart, "denied", shellLaunchArgs, map[string]any{"task_id": "shell_y", "backgrounded": true, "status": "Running"}))
	assert.Empty(t, sink.BackgroundTasks())
}

// fakeShellJobs serves the jobs routes of 0.10.0 as the runtime's ShellManager
// does. A list evicts each finished job that started more than an hour ago, and
// only then builds its answer (`list_jobs`, FINISHED_SHELL_MAX_AGE); a read of
// one job evicts nothing (`inspect_job`).
type fakeShellJobs struct {
	mu      sync.Mutex
	status  string
	old     bool
	evicted bool
}

func serveShellJobs(rt *fakeRuntime) *fakeShellJobs {
	jobs := &fakeShellJobs{status: "Running"}
	rt.handle(http.MethodGet, threadPath(testThreadID, threadRouteJobs), func(w http.ResponseWriter, _ *http.Request) {
		jobs.mu.Lock()
		defer jobs.mu.Unlock()
		if jobs.old && jobs.status != "Running" {
			jobs.evicted = true
		}
		listed := []map[string]any{{"id": "shell_other", "job_id": "shell_other", "status": "Completed"}}
		if !jobs.evicted {
			listed = append(listed, map[string]any{"id": testShellTask, "job_id": testShellTask, "status": jobs.status})
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"jobs": listed})
	})
	rt.handle(http.MethodGet, shellJobRoute(testThreadID, testShellTask), func(w http.ResponseWriter, _ *http.Request) {
		jobs.mu.Lock()
		defer jobs.mu.Unlock()
		if jobs.evicted {
			writeFakeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "Job " + testShellTask + " not found", "status": 404}})
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"job": map[string]any{"id": testShellTask, "job_id": testShellTask, "status": jobs.status, "exit_code": 1}})
	})
	return jobs
}

func (j *fakeShellJobs) set(update func(*fakeShellJobs)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	update(j)
}

// startJobsPoller launches one job and returns once the poller armed its first
// tick.
func startJobsPoller(t *testing.T, rt *fakeRuntime) (*Agent, *agenttest.ControlSink, *quartz.Mock, *quartz.Trap, context.Context) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, rt, clock)
	poll := clock.Trap().NewTimer(shellPollTimerTag)
	t.Cleanup(poll.Close)
	ctx := testutil.DeadlineContext(t)
	launchShellJob(a, 1, "call_start")
	assert.Equal(t, shellPollInterval, testutil.WaitForTimer(t, ctx, poll))
	return a, sink, clock, poll, ctx
}

// pollerStopped waits until the poller ends.
func pollerStopped(t *testing.T, a *Agent) {
	t.Helper()
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.shells.polling
	}, 30*time.Second, 5*time.Millisecond, "the poller ends once no job runs")
}

// From 0.10.0 the runtime lists the thread's jobs, and a poller reads the end
// that the event stream never states.
func TestTheJobsPollerClosesAJobThatEnded(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	jobs := serveShellJobs(rt)
	a, sink, clock, poll, ctx := startJobsPoller(t, rt)

	// The first tick finds the job running, so the poller arms again.
	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	task, _ := sink.BackgroundTask("call_start")
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	assert.Len(t, sink.BackgroundTasks(), 1, "a job the thread never started opens no row")

	jobs.set(func(j *fakeShellJobs) { j.status = "Failed" })
	clock.Advance(shellPollInterval).MustWait(ctx)
	pollerStopped(t, a)
	task, _ = sink.BackgroundTask("call_start")
	assert.Equal(t, bgtask.StatusFailed, task.Status)

	// A later job starts a new poller, which knows the routes already.
	jobs.set(func(j *fakeShellJobs) { j.status = "Running" })
	launchShellJob(a, 30, "call_again")
	testutil.WaitForTimer(t, ctx, poll)
	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	assert.Len(t, rt.requestsTo(http.MethodGet, threadPath(testThreadID, threadRouteJobs)), 1, "the list is read once, to learn that the routes exist")
	assert.Len(t, rt.requestsTo(http.MethodGet, shellJobRoute(testThreadID, testShellTask)), 3)
}

// A list that fails for any reason but a missing route establishes nothing, so
// the next tick asks again.
func TestTheJobsPollerAsksAgainAfterAFailedProbe(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	serveShellJobs(rt)
	listRoute := threadPath(testThreadID, threadRouteJobs)
	var failed atomic.Bool
	served := rt.handlerFor(http.MethodGet, listRoute)
	rt.handle(http.MethodGet, listRoute, func(w http.ResponseWriter, r *http.Request) {
		if failed.CompareAndSwap(false, true) {
			writeFakeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "busy", "status": 500}})
			return
		}
		served(w, r)
	})
	a, _, clock, poll, ctx := startJobsPoller(t, rt)

	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	a.Mu.Lock()
	routes := a.shells.jobsRoutes
	a.Mu.Unlock()
	assert.Equal(t, jobsRoutesUnknown, routes)

	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	a.Mu.Lock()
	routes = a.shells.jobsRoutes
	a.Mu.Unlock()
	assert.Equal(t, jobsRoutesServed, routes)
	assert.Len(t, rt.requestsTo(http.MethodGet, listRoute), 2)
}

// The runtime drops a finished job that started more than an hour ago from its
// list in the same call that would state its end. The poller reads the job
// itself, which drops nothing.
func TestTheJobsPollerReadsTheEndOfAJobThatRanForAnHour(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	jobs := serveShellJobs(rt)
	a, sink, clock, poll, ctx := startJobsPoller(t, rt)

	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	jobs.set(func(j *fakeShellJobs) {
		j.old = true
		j.status = "Failed"
	})
	clock.Advance(shellPollInterval).MustWait(ctx)
	pollerStopped(t, a)
	task, _ := sink.BackgroundTask("call_start")
	assert.Equal(t, bgtask.StatusFailed, task.Status)
}

// A job that the runtime evicted before the poller read its end has ended: the
// runtime never evicts a job that runs. Its outcome is lost, and the row closes.
func TestTheJobsPollerClosesAJobThatTheRuntimeForgot(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	jobs := serveShellJobs(rt)
	a, sink, clock, poll, ctx := startJobsPoller(t, rt)

	clock.Advance(shellPollInterval).MustWait(ctx)
	testutil.WaitForTimer(t, ctx, poll)
	jobs.set(func(j *fakeShellJobs) {
		j.status = "Completed"
		j.evicted = true
	})
	clock.Advance(shellPollInterval).MustWait(ctx)
	pollerStopped(t, a)
	task, _ := sink.BackgroundTask("call_start")
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
}

// 0.9.13 has no jobs route. One 404 stops the poller for good.
func TestTheJobsPollerStopsOnAMissingRoute(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := threadPath(testThreadID, threadRouteJobs)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, rt, clock)
	poll := clock.Trap().NewTimer(shellPollTimerTag)
	t.Cleanup(poll.Close)
	ctx := testutil.DeadlineContext(t)
	launchShellJob(a, 1, "call_start")

	testutil.WaitForTimer(t, ctx, poll)
	clock.Advance(shellPollInterval).MustWait(ctx)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return a.shells.jobsRoutes == jobsRoutesMissing && !a.shells.polling
	}, 30*time.Second, 5*time.Millisecond)
	assert.Len(t, rt.requestsTo(http.MethodGet, route), 1)

	// A second job starts no poller, and its row stays open until something
	// reports its end.
	a.HandleOutput(toolStartEvent(10, "item_2", "call_second", contracts.CodewhaleToolTaskShellStart, shellLaunchArgs))
	a.HandleOutput(toolEndEvent(11, "item.completed", "item_2", "call_second", contracts.CodewhaleToolTaskShellStart, "started", shellLaunchArgs,
		map[string]any{"task_id": "shell_second", "backgrounded": true, "status": "Running"}))
	a.Mu.Lock()
	polling := a.shells.polling
	a.Mu.Unlock()
	assert.False(t, polling)
	task, _ := sink.BackgroundTask("call_second")
	assert.Equal(t, bgtask.StatusRunning, task.Status)
}

// The runtime ends every job with the process that started it.
func TestAnExitStopsEveryJobThatRuns(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.Mu.Lock()
	a.shells.jobsRoutes = jobsRoutesMissing
	a.Mu.Unlock()
	launchShellJob(a, 1, "call_start")

	a.Stop()
	task, ok := sink.BackgroundTask("call_start")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, task.Status)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.shells.rowKeys)
}

// blockUntilCancelled answers a route only when the request ends, and closes
// entered when the first request arrives. The test releases what is still
// parked when it ends.
//
// Call it AFTER the agent is built. Cleanups run in reverse order, so the
// release then runs before the agent's own cleanup, which waits for every
// watcher and so for the request that the handler holds.
func blockUntilCancelled(t *testing.T, rt *fakeRuntime, method, route string) (entered <-chan struct{}) {
	t.Helper()
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	rt.handle(method, route, func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	return arrived
}

// stopWithin runs Stop, and fails the test when Stop does not return before
// ctx ends.
func stopWithin(t *testing.T, ctx context.Context, a *Agent) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop waited for a watcher's request")
	}
}

// A stop cancels the watchers, and a watcher's request ends with them: a
// runtime that does not answer must not hold Stop for the API timeout.
func TestStopEndsTheRequestOfAChildWatcher(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgentWith(t, rt, testAgentOptions{apiTimeout: time.Hour})
	entered := blockUntilCancelled(t, rt, http.MethodGet, routeAgentRuns+"/"+testChildID)
	ctx := testutil.DeadlineContext(t)
	startChildRun(a)

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the watcher read no run status")
	}
	stopWithin(t, ctx, a)
	task, ok := sink.BackgroundTask(testChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, task.Status)
}

func TestStopEndsTheRequestOfTheJobsPoller(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWith(t, rt, testAgentOptions{clock: clock, apiTimeout: time.Hour})
	entered := blockUntilCancelled(t, rt, http.MethodGet, threadPath(testThreadID, threadRouteJobs))
	poll := clock.Trap().NewTimer(shellPollTimerTag)
	t.Cleanup(poll.Close)
	ctx := testutil.DeadlineContext(t)
	launchShellJob(a, 1, "call_start")

	testutil.WaitForTimer(t, ctx, poll)
	clock.Advance(shellPollInterval).MustWait(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the poller read no jobs")
	}
	stopWithin(t, ctx, a)
	task, ok := sink.BackgroundTask("call_start")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, task.Status)
}
