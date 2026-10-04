package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type qoderChildWriteFailureSink struct {
	*agenttest.Sink
	writes   int
	injected int
}

type qoderFailingChildTranscript struct {
	*agenttest.Sink
	parent *qoderChildWriteFailureSink
}

func (s *qoderChildWriteFailureSink) ChildSink(childID string) agent.ProviderServices {
	return agent.NewProviderServices(&qoderFailingChildTranscript{Sink: s.Child(childID), parent: s})
}

func (s *qoderFailingChildTranscript) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.parent.writes++
	if s.parent.writes == 2 {
		s.parent.injected++
		return errors.New("child archive write failed")
	}
	return s.Sink.PersistMessage(source, content, span)
}

type qoderPromptWriteFailureSink struct {
	*agenttest.Sink
	failTargetTimes int
	targetWrites    int
}

type qoderArchiveSwapRoot struct {
	qoderArchiveReadRoot
	beforeOpen func(string)
	afterLstat func(string)
}

func (r *qoderArchiveSwapRoot) Open(name string) (*os.File, error) {
	if r.beforeOpen != nil {
		r.beforeOpen(name)
	}
	return r.qoderArchiveReadRoot.Open(name)
}

func (r *qoderArchiveSwapRoot) Lstat(name string) (os.FileInfo, error) {
	info, err := r.qoderArchiveReadRoot.Lstat(name)
	if err == nil && r.afterLstat != nil {
		r.afterLstat(name)
	}
	return info, err
}

func (r *qoderArchiveSwapRoot) OpenChild(name string) (sessionstore.ArchiveRoot, error) {
	child, err := r.qoderArchiveReadRoot.OpenChild(name)
	if err != nil {
		return nil, err
	}
	return &qoderArchiveSwapRoot{
		qoderArchiveReadRoot: child,
		beforeOpen:           r.beforeOpen,
		afterLstat:           r.afterLstat,
	}, nil
}

func (s *qoderPromptWriteFailureSink) PersistChildPrompt(childID, prompt string) error {
	if prompt == "Reply with FIRST." {
		s.targetWrites++
		if s.targetWrites <= s.failTargetTimes {
			return errors.New("child prompt write failed")
		}
	}
	return s.Sink.PersistChildPrompt(childID, prompt)
}

type qoderWorkflowFixture struct {
	workingDir string
	homeDir    string
	outputFile string
	journal    string
	firstFile  string
	secondFile string
}

func writeQoderWorkflowFixtureFile(t *testing.T, path, value string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
}

func qoderWorkflowArchiveRow(sessionID, kind, agentID, workingDir, content string) string {
	return fmt.Sprintf(`{"type":%q,"sessionId":%q,"parent_tool_use_id":"run-workflow","agentId":%q,"cwd":%q,"message":{"role":%q,"content":%s}}`, kind, sessionID, agentID, workingDir, kind, content)
}

func newQoderWorkflowFixture(t *testing.T) qoderWorkflowFixture {
	t.Helper()
	workingDir := t.TempDir()
	homeDir := t.TempDir()
	runDir := filepath.Join(workingDir, ".qoder", "sessions", "session-1", "workflows", "runs", "wf_1")
	fixture := qoderWorkflowFixture{
		workingDir: workingDir,
		homeDir:    homeDir,
		outputFile: filepath.Join(runDir, "output.json"),
		journal:    filepath.Join(runDir, "journal.jsonl"),
	}
	writeQoderWorkflowFixtureFile(t, fixture.outputFile, `{"runId":"wf_1","taskId":"wf-1","workflowName":"qoder-e2e-workflow","agentCount":2}`)
	var journal []string
	for _, child := range []struct {
		taskID  string
		agentID string
		prompt  string
		answer  string
	}{
		{taskID: "child-task-1", agentID: "aworkflow-subagent-1", prompt: "Reply with FIRST.", answer: "FIRST"},
		{taskID: "child-task-2", agentID: "aworkflow-subagent-2", prompt: "Reply with SECOND.", answer: "SECOND"},
	} {
		transcript := filepath.Join(homeDir, ".qoder", "projects", qoderProjectSlug(workingDir), "session-1", "subagents", "agent-"+child.agentID+".jsonl")
		if child.taskID == "child-task-1" {
			fixture.firstFile = transcript
		} else {
			fixture.secondFile = transcript
		}
		rows := []string{qoderWorkflowArchiveRow("session-1", "user", child.agentID, workingDir, fmt.Sprintf("%q", child.prompt))}
		if child.taskID == "child-task-1" {
			rows = append(rows,
				qoderWorkflowArchiveRow("session-1", "assistant", child.agentID, workingDir, `[{"type":"tool_use","id":"read-1","name":"Read","input":{"file_path":"note.txt"}}]`),
				qoderWorkflowArchiveRow("session-1", "user", child.agentID, workingDir, `[{"type":"tool_result","tool_use_id":"read-1","content":"FILE_OK"}]`),
			)
		}
		rows = append(rows, qoderWorkflowArchiveRow("session-1", "assistant", child.agentID, workingDir, fmt.Sprintf(`[{"type":"text","text":%q}]`, child.answer)))
		writeQoderWorkflowFixtureFile(t, transcript, strings.Join(rows, "\n")+"\n")
		journal = append(journal, fmt.Sprintf(`{"type":"result","agentId":%q,"result":{"state":"done","outputPath":%q,"transcriptPath":%q}}`,
			child.agentID, filepath.Join(workingDir, "tasks", child.taskID+".output"), transcript))
	}
	writeQoderWorkflowFixtureFile(t, fixture.journal, strings.Join(journal, "\n")+"\n")
	return fixture
}

func (f qoderWorkflowFixture) read(t *testing.T) (qoderWorkflowArchive, error) {
	t.Helper()
	run := &qoderWorkflowRun{runID: "wf_1", label: "Ask two children.", children: map[string]*qoderWorkflowChild{
		"child-task-1": {prompt: "Reply with FIRST."},
		"child-task-2": {prompt: "Reply with SECOND."},
	}}
	return f.readWithRun(t, run)
}

func (f qoderWorkflowFixture) readWithRun(t *testing.T, run *qoderWorkflowRun) (qoderWorkflowArchive, error) {
	t.Helper()
	event := &qoderTaskEvent{TaskID: "wf-1", ToolUseID: "run-workflow", OutputFile: f.outputFile}
	return readQoderWorkflowArchive(agent.Options{HomeDir: f.homeDir, WorkingDir: f.workingDir}, "session-1", event, run)
}

func (f qoderWorkflowFixture) readWithOpener(t *testing.T, run *qoderWorkflowRun,
	opener func(string) (qoderArchiveReadRoot, error),
) (qoderWorkflowArchive, error) {
	t.Helper()
	event := &qoderTaskEvent{TaskID: "wf-1", ToolUseID: "run-workflow", OutputFile: f.outputFile}
	return readQoderWorkflowArchiveWithOpener(
		agent.Options{HomeDir: f.homeDir, WorkingDir: f.workingDir}, "session-1", event, run, opener)
}

func startQoderWorkflowFixture(a *Agent) {
	a.HandleOutput([]byte(qoderWorkflowLaunch))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-1","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"First child","prompt":"Reply with FIRST."}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-2","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"Second child","prompt":"Reply with SECOND."}`))
}

func notifyQoderWorkflow(a *Agent, outputFile string) {
	a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"task_notification","session_id":"session-1","task_id":"wf-1","tool_use_id":"run-workflow","status":"completed","output_file":%q}`, outputFile)))
}

func startQoderSecondSessionWorkflow(a *Agent) {
	// Qoder's /new command emits another init on the same stream-json process.
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-2"}`))
	launch := strings.ReplaceAll(qoderWorkflowLaunch, "wf-1", "wf-2")
	launch = strings.ReplaceAll(launch, "wf_1", "wf_2")
	launch = strings.Replace(launch, `"type":"user"`, `"type":"user","session_id":"session-2"`, 1)
	a.HandleOutput([]byte(launch))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","session_id":"session-2","task_id":"child-task-new","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"New child","prompt":"Reply with NEW."}`))
}

func qoderArchiveJobByTask(a *Agent, taskID string) *qoderArchiveJob {
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()
	for _, job := range a.archiveJobs {
		if job.event.TaskID == taskID {
			return job
		}
	}
	return nil
}

func qoderArchiveJobCount(a *Agent) int {
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()
	return len(a.archiveJobs)
}

func qoderChildCloseSignal(sink *agenttest.Sink, taskID string) <-chan struct{} {
	closed := make(chan struct{}, 1)
	sink.OnCloseBackgroundTask = func(rowKey string, _ bgtask.Status) {
		if rowKey == taskID {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	return closed
}

func awaitQoderChildClose(t *testing.T, ctx context.Context, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("workflow child did not close after the archive attempt")
	}
}

func TestQoderWorkflowArchiveLinksDistinctChildTranscripts(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	archive, err := fixture.read(t)
	require.NoError(t, err)
	assert.Equal(t, "qoder-e2e-workflow", archive.name)
	assert.Equal(t, []string{"child-task-1", "child-task-2"}, archive.order)
	first := archive.children["child-task-1"]
	second := archive.children["child-task-2"]
	assert.Equal(t, bgtask.StatusCompleted, first.status)
	assert.Equal(t, "Reply with FIRST.", first.prompt)
	assert.Len(t, first.messages, 4)
	assert.True(t, first.messages[0].initialPrompt)
	assert.Contains(t, string(first.messages[2].raw), "FILE_OK")
	assert.Len(t, second.messages, 2)
	assert.Contains(t, string(second.messages[1].raw), "SECOND")
}

func TestQoderWorkflowNotificationReplaysEachSavedChild(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	a.sessionID = "session-1"
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status)
	assert.Equal(t, "qoder-e2e-workflow", workflow.GroupLabel)
	first, exists := sink.BackgroundTask("child-task-1")
	require.True(t, exists)
	second, exists := sink.BackgroundTask("child-task-2")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Equal(t, first.Status, second.Status)
	assert.Equal(t, workflow.GroupLabel, first.GroupLabel)
	assert.Equal(t, workflow.GroupLabel, second.GroupLabel)
	firstRows := sink.Child(first.ChildAgentID).Messages()
	secondRows := sink.Child(second.ChildAgentID).Messages()
	require.Len(t, firstRows, 4)
	require.Len(t, secondRows, 2)
	assert.Contains(t, string(firstRows[1].Content), `"tool_use"`)
	assert.Contains(t, string(firstRows[2].Content), "FILE_OK")
	assert.Contains(t, string(firstRows[3].Content), "FIRST")
	assert.NotContains(t, string(firstRows[3].Content), "SECOND")
	assert.Contains(t, string(secondRows[1].Content), "SECOND")
	assert.NotContains(t, string(secondRows[1].Content), "FIRST")
	assert.Len(t, sink.Messages(), 1, "workflow child messages must not enter the root transcript")
}

func TestQoderWorkflowArchiveRetryAfterMissingFirstRead(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	output, err := os.ReadFile(fixture.outputFile)
	require.NoError(t, err)
	require.NoError(t, os.Remove(fixture.outputFile))
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	workflow, found := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status, "the native completion closes the parent")
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, first.Status, "a linked child must stay open until its saved transcript arrives")
	require.Len(t, sink.Child(first.ChildAgentID).Messages(), 1, "the live prompt is not the saved answer")

	writeQoderWorkflowFixtureFile(t, fixture.outputFile, string(output))
	notifyQoderWorkflow(a, fixture.outputFile)
	first, found = sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 4)
}

func TestQoderWorkflowArchiveRetryKeepsOriginalSessionID(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	output, err := os.ReadFile(fixture.outputFile)
	require.NoError(t, err)
	require.NoError(t, os.Remove(fixture.outputFile))
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	require.Equal(t, bgtask.StatusRunning, first.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-2"}`))
	require.Equal(t, "session-2", a.sessionID)
	writeQoderWorkflowFixtureFile(t, fixture.outputFile, string(output))
	notifyQoderWorkflow(a, fixture.outputFile)
	first, found = sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	rows := sink.Child(first.ChildAgentID).Messages()
	require.Len(t, rows, 4, "the saved child history still belongs to session 1")
	assert.Contains(t, string(rows[3].Content), "FIRST")
	assert.Empty(t, sink.LeapMuxNotifications(), "a new active session cannot invalidate the old archive")
}

func TestQoderWorkflowNewSessionKeepsBothRows(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	startQoderSecondSessionWorkflow(a)

	oldWorkflow, found := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, found, "the first session keeps its workflow row")
	assert.Equal(t, bgtask.StatusCompleted, oldWorkflow.Status)
	newWorkflow, found := sink.BackgroundTask("workflow:session-2:run-workflow")
	require.True(t, found, "the new session must not reuse the first workflow row")
	assert.Equal(t, bgtask.StatusRunning, newWorkflow.Status)
	oldChild, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	newChild, found := sink.BackgroundTask("child-task-new")
	require.True(t, found)
	assert.Equal(t, oldWorkflow.RowKey, oldChild.GroupKey)
	assert.Equal(t, newWorkflow.RowKey, newChild.GroupKey)
	assert.NotEqual(t, oldChild.GroupKey, newChild.GroupKey)
}

func TestQoderWorkflowNewSessionKeepsBothArchiveJobs(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	output, err := os.ReadFile(fixture.outputFile)
	require.NoError(t, err)
	require.NoError(t, os.Remove(fixture.outputFile))
	sink := &agenttest.Sink{}
	a := newOfflineAgentWithRuntime(t, sink, testutil.NewQuartzMock(t), nil)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	oldJob := qoderArchiveJobByTask(a, "wf-1")
	require.NotNil(t, oldJob)

	startQoderSecondSessionWorkflow(a)
	secondOutput := filepath.Join(fixture.workingDir, ".qoder", "sessions", "session-2", "workflows", "runs", "wf_2", "output.json")
	a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"task_notification","session_id":"session-2","task_id":"wf-2","tool_use_id":"run-workflow","status":"completed","output_file":%q}`, secondOutput)))
	assert.Equal(t, 2, qoderArchiveJobCount(a), "both sessions can wait for archives under one model tool ID")
	newJob := qoderArchiveJobByTask(a, "wf-2")
	require.NotNil(t, newJob)
	assert.NotSame(t, oldJob, newJob)

	writeQoderWorkflowFixtureFile(t, fixture.outputFile, string(output))
	require.True(t, a.tryWorkflowArchive(oldJob), "the first session's archive is ready")
	assert.Equal(t, 1, qoderArchiveJobCount(a), "the first job clears without removing the second job")
	assert.Same(t, newJob, qoderArchiveJobByTask(a, "wf-2"), "old completion cannot delete the new job")
	a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"task_notification","session_id":"session-1","task_id":"wf-1","tool_use_id":"run-workflow","status":"completed","output_file":%q}`, fixture.outputFile)))
	assert.Same(t, newJob, qoderArchiveJobByTask(a, "wf-2"), "the old duplicate final cannot consume the new job")
	newChild, found := sink.BackgroundTask("child-task-new")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, newChild.Status, "the second child still waits for its own archive")

	const secondAgentID = "aworkflow-subagent-new"
	transcript := filepath.Join(fixture.homeDir, ".qoder", "projects", qoderProjectSlug(fixture.workingDir), "session-2", "subagents", "agent-"+secondAgentID+".jsonl")
	writeQoderWorkflowFixtureFile(t, secondOutput, `{"runId":"wf_2","taskId":"wf-2","workflowName":"second-session-workflow","agentCount":1}`)
	writeQoderWorkflowFixtureFile(t, filepath.Join(filepath.Dir(secondOutput), "journal.jsonl"), fmt.Sprintf(`{"type":"result","agentId":%q,"result":{"state":"done","outputPath":%q,"transcriptPath":%q}}`+"\n",
		secondAgentID, filepath.Join(fixture.workingDir, "tasks", "child-task-new.output"), transcript))
	writeQoderWorkflowFixtureFile(t, transcript, strings.Join([]string{
		qoderWorkflowArchiveRow("session-2", "user", secondAgentID, fixture.workingDir, `"Reply with NEW."`),
		qoderWorkflowArchiveRow("session-2", "assistant", secondAgentID, fixture.workingDir, `[{"type":"text","text":"NEW"}]`),
	}, "\n")+"\n")
	require.True(t, a.tryWorkflowArchive(newJob), "the second session's archive is ready")
	assert.Equal(t, 0, qoderArchiveJobCount(a), "both completed jobs leave the retry index")
	newChild, found = sink.BackgroundTask("child-task-new")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, newChild.Status)
	assert.Equal(t, "workflow:session-2:run-workflow", newChild.GroupKey)
	newRows := sink.Child(newChild.ChildAgentID).Messages()
	require.Len(t, newRows, 2)
	assert.Contains(t, string(newRows[0].Content), "Reply with NEW.")
	assert.Contains(t, string(newRows[1].Content), "NEW")
	assert.NotContains(t, string(newRows[1].Content), "FIRST")
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestQoderWorkflowArchiveRetryDoesNotRoutePartOfTheArchive(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	secondHistory, err := os.ReadFile(fixture.secondFile)
	require.NoError(t, err)
	require.NoError(t, os.Remove(fixture.secondFile))
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, first.Status)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 1, "a partial archive must not replay only the first child")

	writeQoderWorkflowFixtureFile(t, fixture.secondFile, string(secondHistory))
	notifyQoderWorkflow(a, fixture.outputFile)
	first, found = sink.BackgroundTask("child-task-1")
	require.True(t, found)
	second, found := sink.BackgroundTask("child-task-2")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Equal(t, bgtask.StatusCompleted, second.Status)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 4)
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 2)
	notifyQoderWorkflow(a, fixture.outputFile)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 4, "a repeated notification cannot duplicate the first child")
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 2, "a repeated notification cannot duplicate the second child")
}

func TestQoderWorkflowArchiveRetryWaitsForEveryStartedChild(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	writeQoderWorkflowFixtureFile(t, fixture.outputFile,
		`{"runId":"wf_1","taskId":"wf-1","workflowName":"qoder-e2e-workflow","agentCount":1}`)
	journal, err := os.ReadFile(fixture.journal)
	require.NoError(t, err)
	writeQoderWorkflowFixtureFile(t, fixture.journal, string(bytesBeforeNewline(journal)))
	_, err = fixture.read(t)
	require.ErrorIs(t, err, errQoderArchiveIncomplete,
		"a count that agrees with one journal row cannot omit another started child")

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	workflow, found := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status, "the native result closes the parent")
	for _, taskID := range []string{"child-task-1", "child-task-2"} {
		child, found := sink.BackgroundTask(taskID)
		require.True(t, found)
		assert.Equal(t, bgtask.StatusRunning, child.Status, "a missing child transcript keeps every child open")
		assert.Len(t, sink.Child(child.ChildAgentID).Messages(), 1, "a partial archive routes no saved child rows")
	}
}

func TestQoderWorkflowArchiveRetryUsesTheProcessClock(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	output, err := os.ReadFile(fixture.outputFile)
	require.NoError(t, err)
	require.NoError(t, os.Remove(fixture.outputFile))
	clock := testutil.NewQuartzMock(t)
	sink := &agenttest.Sink{}
	closed := qoderChildCloseSignal(sink, "child-task-1")
	a := newOfflineAgentWithRuntime(t, sink, clock, nil)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, qoderArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, qoderArchiveRetryFirst, delay)
	writeQoderWorkflowFixtureFile(t, fixture.outputFile, string(output))
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	awaitQoderChildClose(t, ctx, closed)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 4)
}

func TestQoderWorkflowArchiveRetryEndsAtTheDeadline(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	require.NoError(t, os.Remove(fixture.outputFile))
	clock := testutil.NewQuartzMock(t)
	sink := &agenttest.Sink{}
	closed := qoderChildCloseSignal(sink, "child-task-1")
	a := newOfflineAgentWithRuntime(t, sink, clock, nil)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, qoderArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	elapsed := time.Duration(0)
	for elapsed < qoderArchiveRetryLimit {
		delay := testutil.WaitForTimer(t, ctx, newTimer)
		assert.LessOrEqual(t, delay, qoderArchiveRetryMax)
		testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
		elapsed += delay
	}
	awaitQoderChildClose(t, ctx, closed)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status, "the native result survives an archive timeout")
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 1, "missing history cannot produce a fabricated answer")
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "the user must see why the transcript is incomplete")
}

func TestQoderWorkflowArchiveRetryStopsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	require.NoError(t, os.Remove(fixture.outputFile))
	clock := testutil.NewQuartzMock(t)
	processDone := make(chan struct{})
	sink := &agenttest.Sink{}
	closed := qoderChildCloseSignal(sink, "child-task-1")
	a := newOfflineAgentWithRuntime(t, sink, clock, processDone)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, qoderArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	assert.Equal(t, qoderArchiveRetryFirst, testutil.WaitForTimer(t, ctx, newTimer))
	close(processDone)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	awaitQoderChildClose(t, ctx, closed)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.NotEmpty(t, sink.LeapMuxNotifications())
}

func TestQoderWorkflowArchiveRetryRefusesAnUnsafePathImmediately(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Remove(fixture.firstFile))
	if err := os.Symlink(outside, fixture.firstFile); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Empty(t, a.archiveJobs, "an unsafe path must not enter the retry queue")
	assert.NotEmpty(t, sink.LeapMuxNotifications())
}

func TestQoderWorkflowArchiveWriteFailureKeepsRowsAndReportsFailure(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	failingSink := &qoderChildWriteFailureSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failingSink))
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)
	assert.Equal(t, 1, failingSink.injected, "the test must exercise one child sink write failure")

	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, first.Status, "a truncated child tab cannot show a successful task")
	firstRows := sink.Child(first.ChildAgentID).Messages()
	require.Len(t, firstRows, 2, "the prompt and the first saved row must survive the failed write")
	assert.Contains(t, string(firstRows[1].Content), `"tool_use"`)
	second, found := sink.BackgroundTask("child-task-2")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, second.Status, "the other child keeps its native result")
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 2)
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "the reader needs a visible reason for the incomplete tab")
}

func TestQoderWorkflowArchiveRecoversFailedLivePrompt(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	failingSink := &qoderPromptWriteFailureSink{Sink: sink, failTargetTimes: 1}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failingSink))
	startQoderWorkflowFixture(a)
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Empty(t, sink.Child(first.ChildAgentID).Messages(), "the live prompt write must fail before replay")
	notifyQoderWorkflow(a, fixture.outputFile)

	assert.Equal(t, 2, failingSink.targetWrites, "archive replay must retry the live prompt")
	first, found = sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	rows := sink.Child(first.ChildAgentID).Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Contains(t, string(rows[0].Content), "Reply with FIRST.")
	assert.Empty(t, sink.LeapMuxNotifications(), "a recovered prompt needs no final error notice")
}

func TestQoderWorkflowArchiveReportsFailedPromptReplay(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	failingSink := &qoderPromptWriteFailureSink{Sink: sink, failTargetTimes: 2}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failingSink))
	startQoderWorkflowFixture(a)
	notifyQoderWorkflow(a, fixture.outputFile)

	assert.Equal(t, 2, failingSink.targetWrites, "archive replay must attempt the prompt again")
	first, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, first.Status, "a missing prompt cannot close as success")
	assert.Empty(t, sink.Child(first.ChildAgentID).Messages(), "a failed prompt blocks the rest of that child's replay")
	second, found := sink.BackgroundTask("child-task-2")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, second.Status)
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 2)
	assert.NotEmpty(t, sink.LeapMuxNotifications())
}

func TestQoderWorkflowArchiveRefusesUntrustedFiles(t *testing.T) {
	t.Parallel()
	t.Run("output outside the run", func(t *testing.T) {
		fixture := newQoderWorkflowFixture(t)
		fixture.outputFile = filepath.Join(fixture.workingDir, "elsewhere", "output.json")
		_, err := fixture.read(t)
		require.ErrorContains(t, err, "does not match the run")
	})
	t.Run("symlinked child file", func(t *testing.T) {
		fixture := newQoderWorkflowFixture(t)
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
		require.NoError(t, os.Remove(fixture.firstFile))
		require.NoError(t, os.Symlink(outside, fixture.firstFile))
		_, err := fixture.read(t)
		require.ErrorContains(t, err, "symlink")
	})
	t.Run("duplicate journal child", func(t *testing.T) {
		fixture := newQoderWorkflowFixture(t)
		journal, err := os.ReadFile(fixture.journal)
		require.NoError(t, err)
		first := bytesBeforeNewline(journal)
		require.NoError(t, os.WriteFile(fixture.journal, append(journal, first...), 0o600))
		_, err = fixture.read(t)
		require.ErrorContains(t, err, "repeats a child task ID")
	})
	t.Run("oversized child file", func(t *testing.T) {
		fixture := newQoderWorkflowFixture(t)
		require.NoError(t, os.Truncate(fixture.firstFile, qoderWorkflowChildLimit+1))
		_, err := fixture.read(t)
		require.ErrorContains(t, err, "not regular within")
	})
}

func TestQoderWorkflowRejectsCheckedOpenArchiveSwaps(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		ancestor bool
	}{
		{name: "anchored ancestor ignores an external symlink", ancestor: true},
		{name: "checked child file changes to another regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			target := fixture.firstFile
			oldAnswer, newAnswer := `"text":"FIRST"`, `"text":"EVIL!"`
			if tc.ancestor {
				// Swap on the final child. A later read must not reject the
				// symlink for an unrelated reason before the archive returns.
				target = fixture.secondFile
				oldAnswer, newAnswer = `"text":"SECOND"`, `"text":"EVIL!!"`
			}
			original, err := os.ReadFile(target)
			require.NoError(t, err)
			first, err := os.ReadFile(fixture.firstFile)
			require.NoError(t, err)
			other := strings.Replace(string(original), oldAnswer, newAnswer, 1)
			require.NotEqual(t, string(original), other)
			childFile := filepath.Base(target)
			configRoot := filepath.Join(fixture.homeDir, ".qoder")
			swapped := false
			opener := func(path string) (qoderArchiveReadRoot, error) {
				root, openErr := openQoderArchiveRoot(path)
				if openErr != nil || path != configRoot {
					return root, openErr
				}
				return &qoderArchiveSwapRoot{qoderArchiveReadRoot: root, beforeOpen: func(name string) {
					if filepath.Base(name) != childFile || swapped {
						return
					}
					swapped = true
					if tc.ancestor {
						subagents := filepath.Dir(target)
						held := subagents + "-held"
						outside := t.TempDir()
						require.NoError(t, os.WriteFile(filepath.Join(outside, childFile), []byte(other), 0o600))
						require.NoError(t, os.WriteFile(filepath.Join(outside, filepath.Base(fixture.firstFile)), first, 0o600))
						require.NoError(t, os.Rename(subagents, held))
						if linkErr := os.Symlink(outside, subagents); linkErr != nil {
							t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
						}
					} else {
						require.NoError(t, os.Rename(target, target+".held"))
						require.NoError(t, os.WriteFile(target, []byte(other), 0o600))
					}
				}}, nil
			}
			run := &qoderWorkflowRun{runID: "wf_1", label: "Ask two children.", children: map[string]*qoderWorkflowChild{
				"child-task-1": {prompt: "Reply with FIRST."},
				"child-task-2": {prompt: "Reply with SECOND."},
			}}
			archive, err := fixture.readWithOpener(t, run, opener)
			require.True(t, swapped, "the test must change the path after Lstat")
			if tc.ancestor {
				require.NoError(t, err, "an opened directory keeps the original archive")
				child := archive.children["child-task-2"]
				require.NotEmpty(t, child.messages)
				answer := string(child.messages[len(child.messages)-1].raw)
				assert.Contains(t, answer, "SECOND")
				assert.NotContains(t, answer, "EVIL!!")
				return
			}
			require.Error(t, err, "a changed archive path cannot supply child transcript bytes")
			assert.Empty(t, archive.children)
		})
	}
}

func TestQoderArchiveRejectsInRootAncestorSwap(t *testing.T) {
	rootPath := t.TempDir()
	projects := filepath.Join(rootPath, qoderProjectsDirName)
	require.NoError(t, os.MkdirAll(projects, 0o700))
	const file = "agent-child.jsonl"
	require.NoError(t, os.WriteFile(filepath.Join(projects, file), []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"SAFE"}]}`), 0o600))
	alternate := filepath.Join(rootPath, "alternate")
	require.NoError(t, os.MkdirAll(alternate, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(alternate, file), []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"SUBSTITUTE"}]}`), 0o600))
	root, err := openQoderArchiveRoot(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	swapped := false
	reader := &qoderArchiveSwapRoot{qoderArchiveReadRoot: root, afterLstat: func(name string) {
		if name != qoderProjectsDirName || swapped {
			return
		}
		swapped = true
		require.NoError(t, os.Rename(projects, projects+"-held"))
		if linkErr := os.Symlink("alternate", projects); linkErr != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
		}
	}}
	data, err := qoderReadRegularFile(reader, qoderWorkflowChildLimit, qoderProjectsDirName, file)
	require.True(t, swapped, "the ancestor must change after its Lstat")
	assert.NotContains(t, string(data), "SUBSTITUTE", "the changed path must not supply the other archive")
	require.Error(t, err, "a changed ancestor cannot supply another archive")
	assert.Empty(t, data)
}

func TestQoderWorkflowAllowsConfiguredRootSymlink(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	alias := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(filepath.Join(fixture.homeDir, ".qoder"), alias); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	root, err := openQoderArchiveRoot(alias)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	raw, err := qoderReadRegularFile(root, qoderWorkflowChildLimit,
		qoderProjectsDirName, qoderProjectSlug(fixture.workingDir), "session-1", "subagents", filepath.Base(fixture.firstFile))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "FIRST")
}

func TestQoderWorkflowKeepsOpenedRootWhenConfiguredPathChanges(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	configDir := filepath.Join(fixture.homeDir, ".qoder")
	external := t.TempDir()
	project := qoderProjectSlug(fixture.workingDir)
	externalFirst := filepath.Join(external, "projects", project, "session-1", "subagents", filepath.Base(fixture.firstFile))
	externalSecond := filepath.Join(external, "projects", project, "session-1", "subagents", filepath.Base(fixture.secondFile))
	first, err := os.ReadFile(fixture.firstFile)
	require.NoError(t, err)
	second, err := os.ReadFile(fixture.secondFile)
	require.NoError(t, err)
	other := strings.Replace(string(first), `"text":"FIRST"`, `"text":"EVIL!"`, 1)
	require.NotEqual(t, string(first), other)
	writeQoderWorkflowFixtureFile(t, externalFirst, other)
	writeQoderWorkflowFixtureFile(t, externalSecond, string(second))
	held := configDir + "-held"
	swapped := false
	t.Cleanup(func() {
		if swapped {
			require.NoError(t, os.Remove(configDir))
			require.NoError(t, os.Rename(held, configDir))
		}
	})
	opener := func(path string) (qoderArchiveReadRoot, error) {
		root, openErr := openQoderArchiveRoot(path)
		if openErr != nil || path != configDir {
			return root, openErr
		}
		require.NoError(t, os.Rename(configDir, held))
		if linkErr := os.Symlink(external, configDir); linkErr != nil {
			restoreErr := os.Rename(held, configDir)
			_ = root.Close()
			require.NoError(t, restoreErr)
			t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
		}
		swapped = true
		return root, nil
	}
	run := &qoderWorkflowRun{runID: "wf_1", label: "Ask two children.", children: map[string]*qoderWorkflowChild{
		"child-task-1": {prompt: "Reply with FIRST."},
		"child-task-2": {prompt: "Reply with SECOND."},
	}}
	archive, err := fixture.readWithOpener(t, run, opener)
	require.True(t, swapped)
	require.NoError(t, err)
	firstChild := archive.children["child-task-1"]
	require.NotEmpty(t, firstChild.messages)
	answer := string(firstChild.messages[len(firstChild.messages)-1].raw)
	assert.Contains(t, answer, "FIRST")
	assert.NotContains(t, answer, "EVIL!")
}

func bytesBeforeNewline(value []byte) []byte {
	for index, ch := range value {
		if ch == '\n' {
			return value[:index+1]
		}
	}
	return value
}

func TestQoderWorkflowArchiveRejectsWrongPromptOrder(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	history, err := os.ReadFile(fixture.firstFile)
	require.NoError(t, err)
	rows := strings.Split(strings.TrimSpace(string(history)), "\n")
	require.GreaterOrEqual(t, len(rows), 2)
	rows[0], rows[1] = rows[1], rows[0]
	require.NoError(t, os.WriteFile(fixture.firstFile, []byte(strings.Join(rows, "\n")+"\n"), 0o600))
	_, err = fixture.read(t)
	require.ErrorContains(t, err, "prompt")
}

func TestQoderWorkflowArchiveRefusesDifferentChildSession(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	history, err := os.ReadFile(fixture.firstFile)
	require.NoError(t, err)
	changed := strings.Replace(string(history), `"sessionId":"session-1"`, `"sessionId":"other-session"`, 1)
	require.NotEqual(t, string(history), changed)
	require.NoError(t, os.WriteFile(fixture.firstFile, []byte(changed), 0o600))
	_, err = fixture.read(t)
	require.ErrorContains(t, err, "different task")
}

func TestQoderWorkflowArchiveParsesEmptyRun(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	output, err := json.Marshal(map[string]any{"runId": "wf_1", "taskId": "wf-1", "workflowName": "empty-run", "agentCount": 0})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.outputFile, output, 0o600))
	require.NoError(t, os.WriteFile(fixture.journal, nil, 0o600))
	archive, err := fixture.readWithRun(t, &qoderWorkflowRun{runID: "wf_1", label: "empty-run", children: map[string]*qoderWorkflowChild{}})
	require.NoError(t, err)
	assert.Equal(t, "empty-run", archive.name)
	assert.Empty(t, archive.children)
}

func TestQoderWorkflowArchiveParsesEmptyRunWithoutJournal(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	writeQoderWorkflowFixtureFile(t, fixture.outputFile, `{"runId":"wf_1","taskId":"wf-1","workflowName":"empty-run","agentCount":0}`)
	require.NoError(t, os.Remove(fixture.journal))
	archive, err := fixture.readWithRun(t, &qoderWorkflowRun{runID: "wf_1", label: "empty-run", children: map[string]*qoderWorkflowChild{}})
	require.NoError(t, err)
	assert.Equal(t, "empty-run", archive.name)
	assert.Empty(t, archive.children)
}
