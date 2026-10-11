package codewhale

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
