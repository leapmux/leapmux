package mimo

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workflowEvent(t *testing.T, eventType, sessionID, runID string, extra map[string]any) []byte {
	t.Helper()
	properties := map[string]any{"sessionID": sessionID, "runID": runID}
	for key, value := range extra {
		properties[key] = value
	}
	return eventJSON(t, eventType, properties)
}

func TestWorkflowRun(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a, workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_1", map[string]any{"name": "code review"}))
	row := backgroundTask(t, sink, "mimo-workflow:wf_1")
	assert.Equal(t, bgtask.KindWorkflow, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "code review", row.Title)
	assert.Equal(t, "mimo-workflow:wf_1", row.GroupKey)
	assert.Equal(t, "code review", row.GroupLabel)

	feed(a, workflowEvent(t, eventWorkflowPhase, testSessionID, "wf_1", map[string]any{"title": " Reviewing the diff "}))
	row = backgroundTask(t, sink, "mimo-workflow:wf_1")
	assert.Equal(t, "Reviewing the diff", row.ActiveForm)

	feed(a, workflowEvent(t, eventWorkflowFinished, testSessionID, "wf_1", map[string]any{"status": workflowFailed}))
	assert.Equal(t, bgtask.StatusFailed, backgroundTask(t, sink, "mimo-workflow:wf_1").Status)

	feed(a, workflowEvent(t, eventWorkflowPhase, testSessionID, "wf_1", map[string]any{"title": "late"}))
	assert.Equal(t, bgtask.StatusFailed, backgroundTask(t, sink, "mimo-workflow:wf_1").Status, "a finished run stays finished")
}

func TestWorkflowStatus(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bgtask.Status{
		workflowCompleted: bgtask.StatusCompleted,
		workflowFailed:    bgtask.StatusFailed,
		workflowCancelled: bgtask.StatusStopped,
		"":                bgtask.StatusStopped,
		"exploded":        bgtask.StatusStopped,
	} {
		assert.Equal(t, want, workflowStatus(status), "status %q", status)
	}
}

func TestWorkflowWithoutANameIsLabeled(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_2", nil))
	assert.Equal(t, "MiMo workflow", backgroundTask(t, sink, "mimo-workflow:wf_2").Title)
}

func TestWorkflowEventsThatAreNotThisAgents(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a,
		workflowEvent(t, eventWorkflowStarted, "ses_left", "wf_1", map[string]any{"name": "old"}),
		workflowEvent(t, eventWorkflowStarted, testSessionID, "", map[string]any{"name": "no run"}),
	)
	assert.Empty(t, sink.BackgroundTasks())
}

// A finished run takes no more actors. An actor that registers while only one
// run still runs belongs to that run.
func TestActorJoinsTheOneWorkflowThatStillRuns(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a,
		workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_1", map[string]any{"name": "one"}),
		workflowEvent(t, eventWorkflowFinished, testSessionID, "wf_1", map[string]any{"status": workflowCompleted}),
		workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_2", map[string]any{"name": "two"}),
		actorRegisteredEvent(t, "reviewer-1", false),
		actorStatusEvent(t, "reviewer-1", contracts.MiMoActorStatusRunning, "", 0, ""),
	)
	assert.Equal(t, bgtask.StatusCompleted, backgroundTask(t, sink, "mimo-workflow:wf_1").Status)
	row := backgroundTask(t, sink, testSessionID+"/reviewer-1")
	assert.Equal(t, "mimo-workflow:wf_2", row.GroupKey)
	assert.Equal(t, "two", row.GroupLabel)
}

// A workflow's actor has no spawn call, so an actor that a spawn call starts is
// the main agent's own, even when it registers while a workflow runs. MiMo
// registers the actor before the call's update names it, so the call's link
// must undo the guess that the registration made.
func TestSpawnedActorDoesNotJoinARunningWorkflow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_1", map[string]any{"name": "review"}))

	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	row := backgroundTask(t, sink, spawnCallID)
	assert.Empty(t, row.GroupKey)
	assert.Empty(t, row.GroupLabel)

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""))
	assert.Empty(t, backgroundTask(t, sink, spawnCallID).GroupKey, "a later turn keeps the row out of the workflow")
}

func TestMiMoWorkflowLabel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "code review", (&mimoWorkflow{name: " code review\nwith two phases"}).label(), "the first line of the name")
	assert.Equal(t, strings.Repeat("w", 80), (&mimoWorkflow{name: strings.Repeat("w", 200)}).label(), "at most 80 runes")
	assert.Equal(t, "MiMo workflow", (&mimoWorkflow{name: " \n "}).label())
}

// With two runs active, nothing on the wire says which one an actor belongs
// to, so it joins neither group.
func TestActorOfOneOfTwoWorkflowsIsUngrouped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a,
		workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_1", map[string]any{"name": "one"}),
		workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_2", map[string]any{"name": "two"}),
		actorRegisteredEvent(t, "reviewer-1", false),
		actorStatusEvent(t, "reviewer-1", contracts.MiMoActorStatusRunning, "", 0, ""),
	)
	row := backgroundTask(t, sink, testSessionID+"/reviewer-1")
	assert.Empty(t, row.GroupKey)

	a.closeSessionWorkflows(bgtask.StatusFailed)
	for _, runID := range []string{"wf_1", "wf_2"} {
		assert.Equal(t, bgtask.StatusFailed, backgroundTask(t, sink, "mimo-workflow:"+runID).Status, "each run takes the status that the caller gives")
	}
	require.Empty(t, a.workflows)
}
