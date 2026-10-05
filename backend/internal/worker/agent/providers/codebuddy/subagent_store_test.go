package codebuddy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	codebuddyTestWorkflowID = "run-1"
	codebuddyTestChildKey   = "v2:child-key"
	codebuddyTestChildID    = "agent-child-1"
)

type codebuddyChildWriteFailureSink struct {
	*agenttest.Sink
	childID  string
	injected int
}

type codebuddyFailingChildTranscript struct {
	*agenttest.Sink
	parent *codebuddyChildWriteFailureSink
}

type codebuddyArchiveSwapRoot struct {
	codebuddyArchiveReadRoot
	beforeOpen func(string)
	afterLstat func(string)
}

func (r *codebuddyArchiveSwapRoot) Open(name string) (*os.File, error) {
	if r.beforeOpen != nil {
		r.beforeOpen(name)
	}
	return r.codebuddyArchiveReadRoot.Open(name)
}

func (r *codebuddyArchiveSwapRoot) Lstat(name string) (os.FileInfo, error) {
	info, err := r.codebuddyArchiveReadRoot.Lstat(name)
	if err == nil && r.afterLstat != nil {
		r.afterLstat(name)
	}
	return info, err
}

func (r *codebuddyArchiveSwapRoot) OpenChild(name string) (sessionstore.ArchiveRoot, error) {
	child, err := r.codebuddyArchiveReadRoot.OpenChild(name)
	if err != nil {
		return nil, err
	}
	return &codebuddyArchiveSwapRoot{
		codebuddyArchiveReadRoot: child,
		beforeOpen:               r.beforeOpen,
		afterLstat:               r.afterLstat,
	}, nil
}

func (s *codebuddyChildWriteFailureSink) ChildSink(childID string) agent.ProviderServices {
	s.childID = childID
	return agent.NewProviderServices(&codebuddyFailingChildTranscript{Sink: s.Child(childID), parent: s})
}

func (s *codebuddyFailingChildTranscript) PersistMessage(_ leapmuxv1.MessageSource, _ agent.MessageContent, _ agent.SpanInfo) error {
	s.parent.injected++
	return errors.New("child transcript write failed")
}

func codebuddyWorkflowStoreFixture(t *testing.T) (*Agent, *agenttest.Sink, string) {
	return codebuddyWorkflowStoreFixtureWithRuntime(t, nil, nil)
}

func codebuddyWorkflowStoreFixtureWithRuntime(t *testing.T, clock quartz.Clock, processDone chan struct{}) (*Agent, *agenttest.Sink, string) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".codebuddy")
	t.Setenv("CODEBUDDY_CONFIG_DIR", configDir)
	workingDir := filepath.Join(home, "workspace")
	require.NoError(t, os.MkdirAll(workingDir, 0o755))
	sink := &agenttest.Sink{}
	a := newOfflineAgentWithRuntime(t, sink, clock, processDone)
	a.opts.WorkingDir = workingDir
	a.sessionID = "parent-1"
	sessionDir := filepath.Join(configDir, "projects", codebuddyProjectSlug(workingDir), a.sessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "subagents", "workflows", "wf_"+codebuddyTestWorkflowID), 0o755))
	return a, sink, sessionDir
}

func writeCodebuddyWorkflowJournal(t *testing.T, sessionDir string, finishedLines ...string) {
	t.Helper()
	lines := []string{
		`{"type":"run_started","runId":"run-1"}`,
		`{"type":"agent_started","runId":"run-1","key":"v2:child-key","label":"Probe child"}`,
	}
	lines = append(lines, finishedLines...)
	path := filepath.Join(sessionDir, "subagents", "workflows", "wf_"+codebuddyTestWorkflowID, "journal.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func writeCodebuddyChildHistory(t *testing.T, sessionDir, childID string) {
	writeCodebuddyChildHistoryWithText(t, sessionDir, childID, "Reply with CHILD_TEXT.", "CHILD_TEXT")
}

func writeCodebuddyChildHistoryWithText(t *testing.T, sessionDir, childID, prompt, answer string) {
	t.Helper()
	writeCodebuddyChildHistoryRecords(t, sessionDir, childID,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":`+fmt.Sprintf("%q", prompt)+`}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+fmt.Sprintf("%q", answer)+`}]}`)
}

func writeCodebuddyChildHistoryRecords(t *testing.T, sessionDir, childID string, records ...string) {
	t.Helper()
	content := strings.Join(records, "\n") + "\n"
	path := filepath.Join(sessionDir, "subagents", childID+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func finishCodebuddyWorkflowFixture(a *Agent) {
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"run-1","tool_use_id":"run-workflow","task_type":"local_workflow","workflow_name":"Probe workflow"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"run-1","workflow_progress":[{"type":"workflow_agent","agentId":"v2:child-key","state":"start","label":"Probe child"}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
}

func codebuddyChildCloseSignal(sink *agenttest.Sink, rowKey string) <-chan struct{} {
	closed := make(chan struct{}, 1)
	sink.OnCloseBackgroundTask = func(closedKey string, _ bgtask.Status) {
		if closedKey == rowKey {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	return closed
}

func awaitCodebuddyChildClose(t *testing.T, ctx context.Context, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("workflow child did not close after the archive attempt")
	}
}

// settledCodebuddyArchive waits until every archive retry of the agent
// returned, and then copies the two ownership maps under their lock.
//
// A read at the close signal alone races the retry. The retry closes a child
// first and releases the child's ownership after that, on its own goroutine, so
// an unlocked read there is a data race, and a locked read can still see the
// child.
func settledCodebuddyArchive(t *testing.T, a *Agent) (jobs, childJobs map[string]*codebuddyArchiveJob) {
	t.Helper()
	settled := make(chan struct{})
	go func() {
		a.archiveRetries.Wait()
		close(settled)
	}()
	select {
	case <-settled:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("a workflow archive retry did not return")
	}
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()
	return maps.Clone(a.archiveJobs), maps.Clone(a.archiveChildJobs)
}

func TestCodebuddyWorkflowReplaysTheLinkedChildHistoryInOrder(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	finishCodebuddyWorkflowFixture(a)

	row, ok := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, ok)
	require.NotEmpty(t, row.ChildAgentID)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source)
	assert.Contains(t, string(messages[0].Content), "Reply with CHILD_TEXT.")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[1].Source)
	assert.Contains(t, string(messages[1].Content), "CHILD_TEXT")
	assert.Equal(t, 0, sink.MessageCount(), "child history stays out of the parent transcript")
}

func TestCodebuddyWorkflowReplaysNativeChildToolRecords(t *testing.T) {
	for _, tc := range []struct {
		name       string
		callField  string
		resultType string
	}{
		{name: "native callId and function_call_result", callField: "callId", resultType: "function_call_result"},
		{name: "converter call_id and function_call_output", callField: "call_id", resultType: "function_call_output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
			writeCodebuddyWorkflowJournal(t, sessionDir,
				`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
			request := fmt.Sprintf(`{"type":"function_call","id":"request-record-1",%q:"child-read-1","name":"Read","arguments":"{\"file_path\":\"/work/marker.txt\"}"}`, tc.callField)
			result := fmt.Sprintf(`{"type":%q,"id":"result-record-2",%q:"child-read-1","name":"Read","status":"completed","output":"CHILD_FILE_MARKER"}`, tc.resultType, tc.callField)
			writeCodebuddyChildHistoryRecords(t, sessionDir, codebuddyTestChildID,
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Read the marker file."}]}`,
				request,
				result,
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The marker is CHILD_FILE_MARKER."}]}`)
			finishCodebuddyWorkflowFixture(a)

			row, ok := sink.BackgroundTask(codebuddyTestChildKey)
			require.True(t, ok)
			require.NotEmpty(t, row.ChildAgentID)
			messages := sink.Child(row.ChildAgentID).Messages()
			require.Len(t, messages, 4, "the child keeps each native tool record in archive order")
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[1].Source)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[2].Source)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[3].Source)
			assert.JSONEq(t, request, string(messages[1].Content))
			assert.JSONEq(t, result, string(messages[2].Content))
			assert.Equal(t, "child-read-1", messages[1].SpanID)
			assert.Equal(t, "child-read-1", messages[2].SpanID)
			assert.Equal(t, "Read", messages[1].SpanType)
			assert.False(t, messages[1].Closing)
			assert.True(t, messages[2].Closing)
			assert.Contains(t, string(messages[3].Content), "The marker is CHILD_FILE_MARKER.")
			assert.Equal(t, 0, sink.MessageCount(), "the parent transcript keeps no child tool rows")
		})
	}
}

func TestCodebuddyWorkflowRejectsNativeToolWithoutCallID(t *testing.T) {
	history := []byte(strings.Join([]string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Read the marker file."}]}`,
		`{"type":"function_call","id":"record-id-not-call-id","name":"Read","arguments":"{}"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`,
	}, "\n") + "\n")
	_, err := codebuddyArchiveRecords(history)
	require.ErrorContains(t, err, "call ID")
}

func TestCodebuddyWorkflowKeepsNativeProgressBeforeFinalToolResult(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistoryRecords(t, sessionDir, codebuddyTestChildID,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Run the child tool."}]}`,
		`{"type":"function_call","id":"request-record","callId":"child-read-1","name":"Read","arguments":"{}"}`,
		`{"type":"function_call_result","id":"progress-record","callId":"child-read-1","name":"Read","status":"in_progress","output":{"type":"text","text":"partial"}}`,
		`{"type":"function_call_result","id":"final-record","callId":"child-read-1","name":"Read","status":"completed","output":{"type":"text","text":"final"}}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`)
	finishCodebuddyWorkflowFixture(a)

	row, ok := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, ok)
	require.NotEmpty(t, row.ChildAgentID)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 5)
	for _, index := range []int{1, 2, 3} {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[index].Source)
		assert.Equal(t, "child-read-1", messages[index].SpanID)
	}
	assert.Contains(t, string(messages[2].Content), `"in_progress"`)
	assert.Contains(t, string(messages[3].Content), `"completed"`)
	assert.False(t, messages[1].Closing)
	assert.False(t, messages[2].Closing)
	assert.True(t, messages[3].Closing)
}

func TestCodebuddyWorkflowRejectsUnpairedNativeToolRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []string
		want    string
	}{
		{
			name:    "output without a call",
			records: []string{`{"type":"function_call_output","call_id":"missing-call","output":"orphan"}`},
			want:    "no matching call",
		},
		{
			name: "duplicate call ID",
			records: []string{
				`{"type":"function_call","call_id":"shared-call","name":"Read","arguments":"{}"}`,
				`{"type":"function_call","call_id":"shared-call","name":"Bash","arguments":"{}"}`,
			},
			want: "duplicate call ID",
		},
		{
			name: "duplicate output",
			records: []string{
				`{"type":"function_call","call_id":"shared-call","name":"Read","arguments":"{}"}`,
				`{"type":"function_call_output","call_id":"shared-call","output":"first"}`,
				`{"type":"function_call_result","call_id":"shared-call","output":"second"}`,
			},
			want: "duplicate result",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []string{`{"type":"message","role":"user","content":[{"type":"input_text","text":"Run one tool."}]}`}
			records = append(records, tc.records...)
			records = append(records, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`)
			_, err := codebuddyArchiveRecords([]byte(strings.Join(records, "\n") + "\n"))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestCodebuddyWorkflowRejectsConflictingCallIDFields(t *testing.T) {
	history := []byte(strings.Join([]string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Read the marker file."}]}`,
		`{"type":"function_call","callId":"native-call","call_id":"different-call","name":"Read","arguments":"{}"}`,
		`{"type":"function_call_result","callId":"native-call","status":"completed","output":"done"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`,
	}, "\n") + "\n")
	_, err := codebuddyArchiveRecords(history)
	require.ErrorContains(t, err, "conflicting call IDs")
}

func TestCodebuddyWorkflowWaitsForAFinalNativeToolResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result string
	}{
		{name: "no result"},
		{name: "progress only", result: `{"type":"function_call_result","callId":"child-read-1","status":"in_progress","output":"partial"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []string{
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Run the tool."}]}`,
				`{"type":"function_call","callId":"child-read-1","name":"Read","arguments":"{}"}`,
			}
			if tc.result != "" {
				records = append(records, tc.result)
			}
			records = append(records, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`)
			_, err := codebuddyArchiveRecords([]byte(strings.Join(records, "\n") + "\n"))
			require.ErrorIs(t, err, errCodebuddyArchiveIncomplete)
			require.ErrorContains(t, err, "no final result")
		})
	}
}

func TestCodebuddyWorkflowDuplicateFinalKeepsTheFirstOutcome(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"run-1","tool_use_id":"run-workflow","task_type":"local_workflow","workflow_name":"Probe workflow"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"run-1","workflow_progress":[{"type":"workflow_agent","agentId":"v2:child-key","state":"completed","label":"Probe child"}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed","summary":"First summary"}`))

	workflow, found := sink.BackgroundTask(codebuddyTestWorkflowID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status)
	assert.Equal(t, "First summary", workflow.ActiveForm)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	require.NotEmpty(t, child.ChildAgentID)
	assert.Len(t, sink.Child(child.ChildAgentID).Messages(), 2)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"failed","summary":"Conflicting duplicate"}`))
	workflow, found = sink.BackgroundTask(codebuddyTestWorkflowID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status, "a duplicate cannot replace the first final status")
	assert.Equal(t, "First summary", workflow.ActiveForm, "a duplicate cannot erase the first summary")
	assert.Len(t, sink.Child(child.ChildAgentID).Messages(), 2, "a duplicate cannot replay child history")
}

func TestCodebuddyWorkflowLateProgressCannotOpenARunningChild(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	finishCodebuddyWorkflowFixture(a)
	workflow, found := sink.BackgroundTask(codebuddyTestWorkflowID)
	require.True(t, found)
	require.Equal(t, bgtask.StatusCompleted, workflow.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"run-1","workflow_progress":[{"type":"workflow_agent","agentId":"v2:late-child","state":"start","label":"Late child"}]}`))
	_, found = sink.BackgroundTask("v2:late-child")
	assert.False(t, found, "progress after a final workflow cannot leave a child running forever")
}

func TestCodebuddyWorkflowChildWriteFailureReportsFailedWithoutLosingThePrompt(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	failingSink := &codebuddyChildWriteFailureSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failingSink))
	finishCodebuddyWorkflowFixture(a)

	assert.Equal(t, 1, failingSink.injected, "the test must fail exactly one child sink write")
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, child.Status, "a missing answer cannot appear as a successful child")
	require.NotEmpty(t, child.ChildAgentID, "the saved prompt remains linked for inspection")
	require.NotEmpty(t, failingSink.childID)
	assert.Equal(t, failingSink.childID, child.ChildAgentID)
	messages := sink.Child(child.ChildAgentID).Messages()
	require.Len(t, messages, 1, "the prompt written before the error must survive")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source, "no assistant answer was written")
	assert.Contains(t, string(messages[0].Content), "Reply with CHILD_TEXT.")
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "the reader needs a visible route failure")
}

func TestCodebuddyWorkflowArchiveRetryAfterMissingFirstRead(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir)
	finishCodebuddyWorkflowFixture(a)

	workflow, found := sink.BackgroundTask(codebuddyTestWorkflowID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status, "the native completion closes the parent")
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, child.Status, "the child waits for its saved transcript")
	assert.Empty(t, child.ChildAgentID, "an incomplete archive must not open a tab")

	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	child, found = sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	require.NotEmpty(t, child.ChildAgentID)
	require.Len(t, sink.Child(child.ChildAgentID).Messages(), 2)
}

func TestCodebuddyWorkflowArchiveRetryKeepsOriginalSessionID(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	finishCodebuddyWorkflowFixture(a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	require.Equal(t, bgtask.StatusRunning, child.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"parent-2"}`))
	require.Equal(t, "parent-2", a.sessionID)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	child, found = sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	require.NotEmpty(t, child.ChildAgentID)
	rows := sink.Child(child.ChildAgentID).Messages()
	require.Len(t, rows, 2, "the saved child history still belongs to parent session 1")
	assert.Contains(t, string(rows[1].Content), "CHILD_TEXT")
	assert.Empty(t, sink.LeapMuxNotifications(), "a new active session cannot invalidate the old archive")
}

func TestCodebuddyPendingWorkflowChildNotificationWaitsForArchive(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	finishCodebuddyWorkflowFixture(a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	require.Equal(t, bgtask.StatusRunning, child.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"v2:child-key","status":"completed"}`))
	child, found = sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, child.Status, "a child notice cannot close the row before archive replay")
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning}, sink.BackgroundTaskStatuses(codebuddyTestChildKey))

	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	child, found = sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	require.NotEmpty(t, child.ChildAgentID)
	assert.Len(t, sink.Child(child.ChildAgentID).Messages(), 2)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, sink.BackgroundTaskStatuses(codebuddyTestChildKey),
		"the child reaches a final status once, after the saved messages arrive")
	_, childJobs := settledCodebuddyArchive(t, a)
	assert.Empty(t, childJobs, "the completed retry releases child ownership")
}

func TestCodebuddyWorkflowArchiveRetryKeepsPartialRoutesDistinct(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_started","runId":"run-1","key":"v2:child-two","label":"Second child"}`,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-two","sessionId":"agent-child-2"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"run-1","tool_use_id":"run-workflow","task_type":"local_workflow","workflow_name":"Probe workflow"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"run-1","workflow_progress":[{"type":"workflow_agent","agentId":"v2:child-key","state":"completed","label":"Probe child"},{"type":"workflow_agent","agentId":"v2:child-two","state":"failed","label":"Second child"}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))

	first, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	require.NotEmpty(t, first.ChildAgentID, "the complete first child can open its tab")
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 2)
	second, found := sink.BackgroundTask("v2:child-two")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, second.Status, "the missing second file must not close its row")
	assert.Empty(t, second.ChildAgentID)

	writeCodebuddyChildHistoryWithText(t, sessionDir, "agent-child-2", "Reply with SECOND.", "SECOND")
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	first, found = sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	second, found = sink.BackgroundTask("v2:child-two")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, first.Status)
	assert.Equal(t, bgtask.StatusFailed, second.Status, "the child keeps its own native outcome")
	require.NotEmpty(t, second.ChildAgentID)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 2, "retry must not duplicate the first child's history")
	secondMessages := sink.Child(second.ChildAgentID).Messages()
	require.Len(t, secondMessages, 2)
	assert.Contains(t, string(secondMessages[0].Content), "Reply with SECOND.")
	assert.Contains(t, string(secondMessages[1].Content), "SECOND")
}

func TestCodebuddyWorkflowArchiveRetryUsesTheProcessClock(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink, sessionDir := codebuddyWorkflowStoreFixtureWithRuntime(t, clock, nil)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, codebuddyArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	writeCodebuddyWorkflowJournal(t, sessionDir)
	closed := codebuddyChildCloseSignal(sink, codebuddyTestChildKey)
	finishCodebuddyWorkflowFixture(a)

	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, codebuddyArchiveRetryFirst, delay)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	awaitCodebuddyChildClose(t, ctx, closed)
	// The close signal fires before the sink writes the row, so a read here
	// races the retry. Wait for the retry to return first.
	_, _ = settledCodebuddyArchive(t, a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	require.NotEmpty(t, child.ChildAgentID)
	assert.Len(t, sink.Child(child.ChildAgentID).Messages(), 2)
}

func TestCodebuddyWorkflowArchiveRetryEndsAtTheDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink, sessionDir := codebuddyWorkflowStoreFixtureWithRuntime(t, clock, nil)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, codebuddyArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	writeCodebuddyWorkflowJournal(t, sessionDir)
	closed := codebuddyChildCloseSignal(sink, codebuddyTestChildKey)
	finishCodebuddyWorkflowFixture(a)

	elapsed := time.Duration(0)
	for elapsed < codebuddyArchiveRetryLimit {
		delay := testutil.WaitForTimer(t, ctx, newTimer)
		assert.LessOrEqual(t, delay, codebuddyArchiveRetryMax)
		testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
		elapsed += delay
	}
	awaitCodebuddyChildClose(t, ctx, closed)
	// The close signal fires before the sink writes the row, so a read here
	// races the retry. Wait for the retry to return first.
	_, childJobs := settledCodebuddyArchive(t, a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	assert.Empty(t, child.ChildAgentID, "missing history cannot open an empty tab")
	assert.NotEmpty(t, sink.LeapMuxNotifications())
	assert.Empty(t, childJobs, "the retry deadline releases child ownership")
}

func TestCodebuddyWorkflowArchiveRetryStopsWhenTheProcessExits(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	processDone := make(chan struct{})
	a, sink, sessionDir := codebuddyWorkflowStoreFixtureWithRuntime(t, clock, processDone)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, codebuddyArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	writeCodebuddyWorkflowJournal(t, sessionDir)
	closed := codebuddyChildCloseSignal(sink, codebuddyTestChildKey)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"run-1","tool_use_id":"run-workflow","task_type":"local_workflow","workflow_name":"Probe workflow"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"run-1","workflow_progress":[{"type":"workflow_agent","agentId":"v2:child-key","state":"failed","label":"Probe child"}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"run-1","status":"completed"}`))
	assert.Equal(t, codebuddyArchiveRetryFirst, testutil.WaitForTimer(t, ctx, newTimer))
	close(processDone)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	awaitCodebuddyChildClose(t, ctx, closed)
	// The close signal fires before the sink writes the row, so a read here
	// races the retry. Wait for the retry to return first.
	_, childJobs := settledCodebuddyArchive(t, a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, child.Status, "process exit cannot replace the child's native failure")
	assert.NotEmpty(t, sink.LeapMuxNotifications())
	assert.Empty(t, childJobs, "process exit releases child ownership")
}

func TestCodebuddyWorkflowArchiveRetryRefusesAnUnsafePathImmediately(t *testing.T) {
	a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	require.NoError(t, os.WriteFile(outside, []byte(`{"type":"message","role":"assistant"}`), 0o600))
	link := filepath.Join(sessionDir, "subagents", codebuddyTestChildID+".jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	finishCodebuddyWorkflowFixture(a)
	child, found := sink.BackgroundTask(codebuddyTestChildKey)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
	assert.Empty(t, child.ChildAgentID)
	jobs, _ := settledCodebuddyArchive(t, a)
	assert.Empty(t, jobs, "an unsafe path must not enter the retry queue")
	assert.NotEmpty(t, sink.LeapMuxNotifications())
}

func TestCodebuddyWorkflowDoesNotLinkMissingOrAmbiguousChildHistory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finished []string
	}{
		{name: "missing finished link"},
		{name: "ambiguous finished links", finished: []string{
			`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`,
			`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-2"}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
			writeCodebuddyWorkflowJournal(t, sessionDir, tc.finished...)
			writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
			finishCodebuddyWorkflowFixture(a)

			row, ok := sink.BackgroundTask(codebuddyTestChildKey)
			require.True(t, ok)
			assert.Empty(t, row.ChildAgentID, "an unproved child history must not open an empty tab")
		})
	}
}

func TestCodebuddyWorkflowRejectsTraversalAndSymlinkedChildHistory(t *testing.T) {
	t.Run("path traversal", func(t *testing.T) {
		a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
		writeCodebuddyWorkflowJournal(t, sessionDir,
			`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"../outside"}`)
		finishCodebuddyWorkflowFixture(a)

		row, ok := sink.BackgroundTask(codebuddyTestChildKey)
		require.True(t, ok)
		assert.Empty(t, row.ChildAgentID)
	})
	t.Run("symlinked transcript", func(t *testing.T) {
		a, sink, sessionDir := codebuddyWorkflowStoreFixture(t)
		writeCodebuddyWorkflowJournal(t, sessionDir,
			`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		require.NoError(t, os.WriteFile(outside, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SECRET"}]}`), 0o600))
		link := filepath.Join(sessionDir, "subagents", codebuddyTestChildID+".jsonl")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", err)
		}
		finishCodebuddyWorkflowFixture(a)

		row, ok := sink.BackgroundTask(codebuddyTestChildKey)
		require.True(t, ok)
		assert.Empty(t, row.ChildAgentID)
	})
}

func TestCodebuddyWorkflowRejectsCheckedOpenArchiveSwaps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ancestor bool
	}{
		{name: "anchored ancestor ignores an external symlink", ancestor: true},
		{name: "checked child file changes to another regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, sessionDir := codebuddyWorkflowStoreFixture(t)
			writeCodebuddyWorkflowJournal(t, sessionDir,
				`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
			writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
			childFile := codebuddyTestChildID + ".jsonl"
			childPath := filepath.Join(sessionDir, "subagents", childFile)
			original, err := os.ReadFile(childPath)
			require.NoError(t, err)
			other := bytes.Replace(original, []byte("CHILD_TEXT"), []byte("EVIL_SECRET"), 1)
			require.NotEqual(t, original, other)
			swapped := false
			opener := func(path string) (codebuddyArchiveReadRoot, error) {
				root, openErr := openCodebuddyArchiveRoot(path)
				if openErr != nil {
					return nil, openErr
				}
				return &codebuddyArchiveSwapRoot{codebuddyArchiveReadRoot: root, beforeOpen: func(name string) {
					if filepath.Base(name) != childFile || swapped {
						return
					}
					swapped = true
					if tc.ancestor {
						subagents := filepath.Dir(childPath)
						held := subagents + "-held"
						outside := t.TempDir()
						require.NoError(t, os.WriteFile(filepath.Join(outside, childFile), other, 0o600))
						require.NoError(t, os.Rename(subagents, held))
						if linkErr := os.Symlink(outside, subagents); linkErr != nil {
							t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
						}
					} else {
						require.NoError(t, os.Rename(childPath, childPath+".held"))
						require.NoError(t, os.WriteFile(childPath, other, 0o600))
					}
				}}, nil
			}
			records, err := readCodebuddyWorkflowChildWithOpener(
				os.Getenv("CODEBUDDY_CONFIG_DIR"), a.opts.WorkingDir, a.sessionID,
				codebuddyTestWorkflowID, codebuddyTestChildKey, opener)
			require.True(t, swapped, "the test must change the path after Lstat")
			if tc.ancestor {
				require.NoError(t, err, "an opened directory keeps the original archive")
				require.Len(t, records, 2)
				assert.Contains(t, string(records[1].Raw), "CHILD_TEXT")
				assert.NotContains(t, string(records[1].Raw), "EVIL_SECRET")
				return
			}
			require.Error(t, err, "a changed archive path cannot supply child transcript bytes")
			assert.Empty(t, records)
		})
	}
}

func TestCodebuddyArchiveRejectsInRootAncestorSwap(t *testing.T) {
	rootPath := t.TempDir()
	projects := filepath.Join(rootPath, codebuddyProjectsDirName)
	require.NoError(t, os.MkdirAll(projects, 0o700))
	const file = "agent-child.jsonl"
	require.NoError(t, os.WriteFile(filepath.Join(projects, file), []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SAFE"}]}`), 0o600))
	alternate := filepath.Join(rootPath, "alternate")
	require.NoError(t, os.MkdirAll(alternate, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(alternate, file), []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUBSTITUTE"}]}`), 0o600))
	root, err := openCodebuddyArchiveRoot(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	swapped := false
	reader := &codebuddyArchiveSwapRoot{codebuddyArchiveReadRoot: root, afterLstat: func(name string) {
		if name != codebuddyProjectsDirName || swapped {
			return
		}
		swapped = true
		require.NoError(t, os.Rename(projects, projects+"-held"))
		if linkErr := os.Symlink("alternate", projects); linkErr != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
		}
	}}
	data, err := codebuddyReadRegularFile(reader, codebuddyChildHistoryLimit, codebuddyProjectsDirName, file)
	require.True(t, swapped, "the ancestor must change after its Lstat")
	assert.NotContains(t, string(data), "SUBSTITUTE", "the changed path must not supply the other archive")
	require.Error(t, err, "a changed ancestor cannot supply another archive")
	assert.Empty(t, data)
}

func TestCodebuddyWorkflowAllowsConfiguredRootSymlink(t *testing.T) {
	a, _, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	alias := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(os.Getenv("CODEBUDDY_CONFIG_DIR"), alias); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	records, err := readCodebuddyWorkflowChild(alias, a.opts.WorkingDir, a.sessionID,
		codebuddyTestWorkflowID, codebuddyTestChildKey)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Contains(t, string(records[1].Raw), "CHILD_TEXT")
}

func TestCodebuddyWorkflowKeepsOpenedRootWhenConfiguredPathChanges(t *testing.T) {
	a, _, sessionDir := codebuddyWorkflowStoreFixture(t)
	writeCodebuddyWorkflowJournal(t, sessionDir,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistory(t, sessionDir, codebuddyTestChildID)
	configDir := os.Getenv("CODEBUDDY_CONFIG_DIR")
	external := t.TempDir()
	externalSession := filepath.Join(external, "projects", codebuddyProjectSlug(a.opts.WorkingDir), a.sessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(externalSession, "subagents", "workflows", "wf_"+codebuddyTestWorkflowID), 0o755))
	writeCodebuddyWorkflowJournal(t, externalSession,
		`{"type":"agent_finished","runId":"run-1","key":"v2:child-key","sessionId":"agent-child-1"}`)
	writeCodebuddyChildHistoryWithText(t, externalSession, codebuddyTestChildID, "Reply with CHILD_TEXT.", "EXTERNAL_SECRET")
	held := configDir + "-held"
	swapped := false
	t.Cleanup(func() {
		if swapped {
			require.NoError(t, os.Remove(configDir))
			require.NoError(t, os.Rename(held, configDir))
		}
	})
	opener := func(path string) (codebuddyArchiveReadRoot, error) {
		root, err := openCodebuddyArchiveRoot(path)
		if err != nil {
			return nil, err
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
	records, err := readCodebuddyWorkflowChildWithOpener(configDir, a.opts.WorkingDir,
		a.sessionID, codebuddyTestWorkflowID, codebuddyTestChildKey, opener)
	require.True(t, swapped)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Contains(t, string(records[1].Raw), "CHILD_TEXT")
	assert.NotContains(t, string(records[1].Raw), "EXTERNAL_SECRET")
}
