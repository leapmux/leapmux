//
// This suite needs a Windows bring-up that only a local Windows run can state:
// this file shares the symlink fixtures of subagent_test.go, which Windows builds without a privilege.
// The suite keeps its full coverage on macOS and Linux.

package codewhale

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodewhaleEvictedShellKeepsItsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	for _, final := range []string{"Completed", "Failed", "Killed", "TimedOut"} {
		t.Run(final, func(t *testing.T) {
			t.Parallel()
			runtime := newFakeRuntime(t)
			jobs := serveShellJobs(runtime)
			a, sink, clock, poll, ctx := startJobsPoller(t, runtime)
			clock.Advance(shellPollInterval).MustWait(ctx)
			testutil.WaitForTimer(t, ctx, poll)
			before, found := sink.BackgroundTask("call_start")
			require.True(t, found)
			require.Equal(t, bgtask.StatusRunning, before.Status)
			jobs.set(func(job *fakeShellJobs) {
				job.status = final
				job.evicted = true
			})
			clock.Advance(shellPollInterval).MustWait(ctx)
			pollerStopped(t, a)
			after, found := sink.BackgroundTask("call_start")
			require.True(t, found)
			assert.Equal(t, before.RowKey, after.RowKey)
			assert.Equal(t, before.Title, after.Title)
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, after.Status)
			assert.False(t, after.Status.IsWorking())
			assert.True(t, after.Status.IsFinished())
			assert.Len(t, sink.BackgroundTasks(), 1)
		})
	}
}

func TestCodewhaleWorkflowEndRequiresAKnownSuccessfulOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		status        string
		includeStatus bool
		ended         bool
		want          bgtask.Status
	}{
		{name: "unknown end", status: "a_later_word", includeStatus: true, ended: true, want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "empty end", includeStatus: true, ended: true, want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "absent outcome", ended: true, want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "known success", status: "completed", includeStatus: true, ended: true, want: bgtask.StatusSucceeded},
		{name: "known partial failure", status: "degraded", includeStatus: true, ended: true, want: bgtask.StatusFailed},
		{name: "known failure", status: "failed", includeStatus: true, ended: true, want: bgtask.StatusFailed},
		{name: "known cancellation", status: "cancelled", includeStatus: true, ended: true, want: bgtask.StatusStopped},
		{name: "unresolved live outcome", status: "a_later_word", includeStatus: true, want: bgtask.StatusRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newTestAgent(t, nil)
			input := map[string]any{"action": "status", "name": "review"}
			metadata := map[string]any{"run_id": "native-run", "terminal": test.ended}
			if test.includeStatus {
				metadata["status"] = test.status
			}
			a.HandleOutput(toolStartEvent(1, "item_w", "call_w", contracts.CodewhaleToolWorkflow, input))
			a.HandleOutput(toolEndEvent(2, "item.completed", "item_w", "call_w", contracts.CodewhaleToolWorkflow, "ok", input, metadata))
			row, found := sink.BackgroundTask("native-run")
			require.True(t, found)
			assert.Equal(t, bgtask.KindWorkflow, row.Kind)
			assert.Equal(t, "review", row.Title)
			assert.Equal(t, "native-run", row.GroupKey)
			assert.Equal(t, test.want, row.Status)
			assert.Equal(t, test.want.IsFinished(), row.Status.IsFinished())
			assert.Equal(t, !test.want.IsFinished(), row.Status.IsWorking())
			assert.Len(t, sink.BackgroundTasks(), 1)
		})
	}
}

type failingReportAdmissionServices struct {
	agent.ProviderServices
	cleanups atomic.Int32
}

func (s *failingReportAdmissionServices) ReserveTranscriptOutput(*agent.OutputQueue, ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	return nil, errors.New("report admission rejected")
}

func (s *failingReportAdmissionServices) CleanupChildAgent(childID string) {
	s.cleanups.Add(1)
	s.ProviderServices.CleanupChildAgent(childID)
}

func TestCodewhaleFinishChildClosesOnNonRetainedReportError(t *testing.T) {
	t.Parallel()
	services := &failingReportAdmissionServices{}
	a, sink := newTestAgentWith(t, nil, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
		services.ProviderServices = inner
		return services
	}})
	child := &codewhaleChild{agentID: testChildID, childID: "child-admit-err", title: "worker"}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:         bgtask.RoleChildTask,
		RowKey:       child.agentID,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: child.childID,
		Status:       bgtask.StatusRunning,
	}))

	a.finishChild(a.sink.ChildSink(child.childID), child, bgtask.StatusSucceeded, "Completed work.")

	row, found := sink.BackgroundTask(child.agentID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "a non-retained report error must not strand the row Running")
	assert.Equal(t, int32(1), services.cleanups.Load(), "cleanup must still run")
}

func TestCodewhaleFinishChildClosesOnCaptureError(t *testing.T) {
	t.Parallel()
	var cleanups atomic.Int32
	a, sink := newTestAgentWith(t, nil, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
		return &captureErrCleanupServices{ProviderServices: inner, cleanups: &cleanups}
	}})
	a.captureReport = func(agent.TranscriptServices, agent.SubagentReportWrite) (agent.CapturedTranscript, bool, error) {
		return agent.CapturedTranscript{}, false, errors.New("capture failed")
	}
	child := &codewhaleChild{agentID: testChildID, childID: "child-capture-err", title: "worker"}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:         bgtask.RoleChildTask,
		RowKey:       child.agentID,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: child.childID,
		Status:       bgtask.StatusRunning,
	}))

	a.finishChild(a.sink.ChildSink(child.childID), child, bgtask.StatusSucceeded, "Completed work.")

	row, found := sink.BackgroundTask(child.agentID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "a report capture error must not strand the row Running")
	assert.Equal(t, int32(1), cleanups.Load(), "cleanup must still run")
}

type captureErrCleanupServices struct {
	agent.ProviderServices
	cleanups *atomic.Int32
}

func (s *captureErrCleanupServices) CleanupChildAgent(childID string) {
	s.cleanups.Add(1)
	s.ProviderServices.CleanupChildAgent(childID)
}
