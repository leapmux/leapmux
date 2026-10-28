package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Direct projection tests install the same canonical item state as the native
// dispatcher. A fixture without a revision represents its next native revision.
func observeCanonicalChildItem(a *Agent, params itemParams, parent *sessionState, _ nativeItem) error {
	current := parent.items[params.Item.ID]
	var previous nativeItem
	if current == nil {
		current = &itemState{deltas: make(map[string]string), cursors: make(map[string]bool)}
		parent.items[params.Item.ID] = current
	} else {
		previous = current.params.Item
	}
	if params.Item.Revision == 0 {
		params.Item.Revision = previous.Revision + 1
	}
	raw, err := json.Marshal(params.Item)
	if err != nil {
		return err
	}
	current.params = params
	current.nativeItemRaw = raw
	a.dispatcher.mu.Lock()
	err = a.observeChildOrTask(params, parent, previous)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	return err
}

type workflowRegistryFailureSink struct {
	*agenttest.Sink
	upsertKey string
	reviveKey string
	remaining int
}

func TestWorkflowChildStatusUsesFailureKindPresence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fields string
		want         bgtask.Status
	}{
		{name: "successful", fields: `"terminal":"completed"`, want: bgtask.StatusSucceeded},
		{name: "failed completion", fields: `"terminal":"completed","failureKind":"native_failure"`, want: bgtask.StatusFailed},
		{name: "empty failure class", fields: `"terminal":"completed","failureKind":""`, want: bgtask.StatusFailed},
		{name: "cancelled", fields: `"terminal":"cancelled","failureKind":"native_failure"`, want: bgtask.StatusStopped},
		{name: "failed", fields: `"terminal":"failed","failureKind":"native_failure"`, want: bgtask.StatusFailed},
		{name: "null failure class", fields: `"terminal":"completed","failureKind":null`, want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "number failure class", fields: `"terminal":"completed","failureKind":0`, want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "object failure class", fields: `"terminal":"completed","failureKind":{}`, want: bgtask.StatusEndedWithUnknownOutcome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var child nativeWorkflowChild
			require.NoError(t, json.Unmarshal([]byte(`{"childId":"child-a","attempt":1,"status":"terminal",`+tc.fields+`}`), &child))
			assert.Equal(t, tc.want, workflowChildStatus(child))
		})
	}
}

func TestWorkflowChildRequiresAnExplicitIntegerAttempt(t *testing.T) {
	for _, value := range []string{"missing", "null", "1.5", `"1"`} {
		t.Run(value, func(t *testing.T) {
			field := `,"attempt":` + value
			if value == "missing" {
				field = ""
			}
			var child nativeWorkflowChild
			require.Error(t, json.Unmarshal([]byte(`{"childId":"child","status":"started"`+field+`}`), &child))
		})
	}
	for _, value := range []int64{0, -2, -9223372036854775808, 9223372036854775807} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			var child nativeWorkflowChild
			require.NoError(t, json.Unmarshal(fmt.Appendf(nil, `{"childId":"child","attempt":%d,"status":"started"}`, value), &child))
			assert.Equal(t, value, child.Attempt)
		})
	}
}

func TestWorkflowRejectsRepeatedAttemptIdentitiesBeforeAnyProjection(t *testing.T) {
	for _, attempts := range [][]int64{{0, 0}, {0, -1, -1}} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			a, sink := testAgent(t)
			var children []nativeWorkflowChild
			for _, attempt := range attempts {
				children = append(children, nativeWorkflowChild{ChildID: "child", Attempt: attempt, Status: workflowChildStarted})
			}
			raw, err := json.Marshal(children)
			require.NoError(t, err)
			feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: raw}})
			assert.Empty(t, sink.BackgroundTasks())
		})
	}
}

func TestWorkflowAndAddressableChildKeepTheNativeFailure(t *testing.T) {
	for _, failure := range []string{"native_failure", ""} {
		for _, workflowFirst := range []bool{false, true} {
			t.Run(failure+fmt.Sprint(workflowFirst), func(t *testing.T) {
				a, sink := testAgent(t)
				a.viewWork.CloseAndWait()
				parent := a.sessions["session"]
				key := museChildRowKey("session", "child-a")
				a.sessions["native-child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childKey: key, childID: "child-agent", parentSessionID: "session", subagentID: "child-a"}
				encodedFailure, err := json.Marshal(failure)
				require.NoError(t, err)
				workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Status: "completed", WorkflowRunID: "run", EntryID: "Probe run", Children: json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"completed","failureKind":` + string(encodedFailure) + `}]`)}}
				var child itemParams
				require.NoError(t, json.Unmarshal([]byte(`{"sessionId":"session","item":{"itemId":"child-item","kind":"subagent","status":"inProgress","controlStatus":"resultReady","subagentId":"child-a","childSessionId":"native-child","objective":"Actual assignment","workflowRunId":"run","result":{"summary":"Native failure","evidenceRefs":[],"artifactRefs":[],"errorKind":`+string(encodedFailure)+`}}}`), &child))
				items := []itemParams{child, workflow}
				if workflowFirst {
					items[0], items[1] = items[1], items[0]
				}
				for _, item := range items {
					require.NoError(t, observeCanonicalChildItem(a, item, parent, nativeItem{}))
				}
				rows := sink.BackgroundTasks()
				require.Len(t, rows, 2)
				row, ok := sink.BackgroundTask(key)
				require.True(t, ok)
				assert.Equal(t, bgtask.StatusFailed, row.Status)
				assert.Equal(t, "child-agent", row.ChildAgentID)
				assert.Equal(t, "Probe run", row.GroupLabel)
			})
		}
	}
}

func TestWorkflowAndAddressableChildShareOneRegistryRow(t *testing.T) {
	a, sink := testAgent(t)
	parent := a.sessions["session"]
	workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Status: "inProgress", WorkflowRunID: "run", EntryID: "Probe run", Children: json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"started"}]`)}}
	require.NoError(t, observeCanonicalChildItem(a, workflow, parent, nativeItem{}))
	key := bgtask.NormalizeRowKey("session:child:child-a")
	a.sessions["native-child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childKey: key, childID: "child-agent", parentSessionID: "session", subagentID: "child-a"}
	var params itemParams
	require.NoError(t, json.Unmarshal([]byte(`{"sessionId":"session","item":{"itemId":"native-child-item","kind":"subagent","status":"inProgress","controlStatus":"resultReady","subagentId":"child-a","childSessionId":"native-child","objective":"Actual assignment","workflowRunId":"run","result":{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}}}`), &params))
	require.NoError(t, observeCanonicalChildItem(a, params, parent, nativeItem{}))
	workflow.Item.Status = "completed"
	workflow.Item.Children = json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"completed"}]`)
	require.NoError(t, observeCanonicalChildItem(a, workflow, parent, nativeItem{}))
	require.NoError(t, observeCanonicalChildItem(a, params, parent, nativeItem{}))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 2, "the workflow fold and native subagent identify the same child")
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, "child-agent", row.ChildAgentID)
	assert.Equal(t, "Actual assignment", row.Title)
	assert.Equal(t, "Probe run", row.GroupLabel)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestWorkflowRetryRevivesTheCanonicalChildRow(t *testing.T) {
	for _, status := range []string{"scheduled", "started"} {
		t.Run(status, func(t *testing.T) {
			a, sink := testAgent(t)
			workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Revision: 1, Status: "inProgress", WorkflowRunID: "run", EntryID: "Retry run", Children: json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"failed"}]`)}}
			feed(t, a, contracts.MuseMethodItemUpdated, workflow)
			key := museChildRowKey("session", "child-a")
			before, exists := sink.BackgroundTask(key)
			require.True(t, exists)
			require.Equal(t, bgtask.StatusFailed, before.Status)
			workflow.Item.Revision = 2
			workflow.Item.Children = json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"failed"},{"childId":"child-a","attempt":2,"status":"` + status + `"}]`)
			feed(t, a, contracts.MuseMethodItemUpdated, workflow)
			row, exists := sink.BackgroundTask(key)
			require.True(t, exists)
			want := bgtask.StatusRunning
			if status == "scheduled" {
				want = bgtask.StatusPending
			}
			assert.Equal(t, want, row.Status)
			assert.True(t, row.EndedAt.IsZero())
			assert.Equal(t, []string{key}, sink.RevivedTasks())
		})
	}
}

func TestWorkflowFoldSelectsTheNewestAttemptIndependentlyOfArrayOrder(t *testing.T) {
	for _, children := range []string{
		`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"failed"},{"childId":"child-a","attempt":2,"status":"terminal","terminal":"completed"}]`,
		`[{"childId":"child-a","attempt":2,"status":"terminal","terminal":"completed"},{"childId":"child-a","attempt":1,"status":"terminal","terminal":"failed"}]`,
	} {
		t.Run(children, func(t *testing.T) {
			a, sink := testAgent(t)
			feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Revision: 1, Status: "inProgress", WorkflowRunID: "run", Children: json.RawMessage(children)}})
			row, exists := sink.BackgroundTask(museChildRowKey("session", "child-a"))
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.Empty(t, sink.RevivedTasks())
		})
	}
}

func TestWorkflowFoldProjectsItsRunAndChildrenWithoutASession(t *testing.T) {
	a, sink := testAgent(t)
	parent := &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog("parent")}
	a.sessions["parent"] = parent
	feedItem := func(status string, children string) {
		require.NoError(t, observeCanonicalChildItem(a, itemParams{SessionID: "parent", Item: nativeItem{
			ID: "workflow-item", Kind: contracts.MuseItemKindWorkflow, Status: status,
			WorkflowRunID: "workflow-run-1", EntryID: "Probe run", Children: json.RawMessage(children),
		}}, parent, nativeItem{}))
	}
	feedItem(contracts.MuseItemStatusInProgress, `[{"childId":"child-a","attempt":1,"status":"started"},{"childId":"child-b","attempt":1,"status":"scheduled"}]`)
	feedItem(contracts.MuseItemStatusCompleted, `[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"completed"},{"childId":"child-b","attempt":1,"status":"terminal","terminal":"failed","failureReason":"native"}]`)
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 3)
	byKey := make(map[string]bgtask.Item, len(tasks))
	for _, task := range tasks {
		byKey[task.RowKey] = task
	}
	run := bgtask.NormalizeRowKey("parent:workflow:workflow-run-1")
	childA := bgtask.NormalizeRowKey("parent:child:child-a")
	childB := bgtask.NormalizeRowKey("parent:child:child-b")
	runRow, ok := byKey[run]
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, runRow.Kind)
	assert.Equal(t, "Probe run", runRow.Title)
	assert.Equal(t, bgtask.StatusSucceeded, runRow.Status)
	assert.Equal(t, run, runRow.GroupKey)
	for key, expected := range map[string]struct {
		status bgtask.Status
		title  string
	}{
		childA: {status: bgtask.StatusSucceeded, title: "Child child-a"},
		childB: {status: bgtask.StatusFailed, title: "Child child-b"},
	} {
		row, ok := byKey[key]
		require.True(t, ok, key)
		assert.Equal(t, expected.status, row.Status, key)
		assert.Equal(t, bgtask.KindSubagent, row.Kind, key)
		assert.Equal(t, expected.title, row.Title, key)
		assert.Equal(t, run, row.GroupKey, key)
		assert.Equal(t, "Probe run", row.GroupLabel, key)
		assert.Empty(t, row.ChildAgentID, "a fold without an addressable session opens no transcript", key)
	}
}

func (s *workflowRegistryFailureSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	if s.remaining > 0 && task.RowKey == s.upsertKey {
		s.remaining--
		return errors.New("registry unavailable")
	}
	return s.Sink.UpsertBackgroundTask(task)
}

func (s *workflowRegistryFailureSink) ReviveBackgroundTask(key string) error {
	if s.remaining > 0 && key == s.reviveKey {
		s.remaining--
		return errors.New("registry unavailable")
	}
	return s.Sink.ReviveBackgroundTask(key)
}

func TestWorkflowRetryKeepsItsAttemptAcrossRegistryRefusals(t *testing.T) {
	for _, failure := range []string{"run", "revival", "child"} {
		t.Run(failure, func(t *testing.T) {
			a, sink := testAgent(t)
			key := museChildRowKey("session", "child")
			workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Revision: 1, Status: "inProgress", WorkflowRunID: "run", Children: json.RawMessage(`[{"childId":"child","attempt":0,"status":"terminal","terminal":"failed"}]`)}}
			feed(t, a, contracts.MuseMethodItemUpdated, workflow)
			failureSink := &workflowRegistryFailureSink{Sink: sink, remaining: 1}
			switch failure {
			case "run":
				failureSink.upsertKey = museWorkflowRowKey("session", "run")
			case "revival":
				failureSink.reviveKey = key
			case "child":
				failureSink.upsertKey = key
			}
			a.sink = agent.NewProviderServices(failureSink)
			workflow.Item.Revision = 2
			workflow.Item.Children = json.RawMessage(`[{"childId":"child","attempt":1,"status":"started"}]`)
			raw := feed(t, a, contracts.MuseMethodItemUpdated, workflow)
			require.Zero(t, failureSink.remaining)
			a.HandleOutput(raw)
			require.NoError(t, agent.OutputQueueFor(a.sink).Flush())
			row, ok := sink.BackgroundTask(key)
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.True(t, row.EndedAt.IsZero())
			assert.Equal(t, []string{key}, sink.RevivedTasks())
			assert.Equal(t, int64(2), a.sessions["session"].items["workflow"].registry.registryRevision)
		})
	}
}

func TestWorkflowRetryKeepsTransitionEvidenceBeforeTheRunCanPublish(t *testing.T) {
	a, sink := testAgent(t)
	key := museChildRowKey("session", "child")
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusFailed}))
	failure := &workflowRegistryFailureSink{Sink: sink, upsertKey: museWorkflowRowKey("session", "run"), remaining: 2}
	a.sink = agent.NewProviderServices(failure)
	item := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":-1,"status":"terminal","terminal":"failed"}]`)}}
	feed(t, a, contracts.MuseMethodItemUpdated, item)
	item.Item.Revision = 2
	item.Item.Children = json.RawMessage(`[{"childId":"child","attempt":0,"status":"started"}]`)
	raw := feed(t, a, contracts.MuseMethodItemUpdated, item)
	require.Zero(t, failure.remaining)
	assert.Empty(t, sink.RevivedTasks())
	a.HandleOutput(raw)
	require.NoError(t, agent.OutputQueueFor(a.sink).Flush())
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []string{key}, sink.RevivedTasks())
}

func TestWorkflowAttemptOrderingUsesSignedValuesWithoutArithmetic(t *testing.T) {
	for _, pair := range [][2]int64{{-2, -1}, {-1, 0}, {0, 1}, {-9223372036854775808, -9223372036854775807}, {9223372036854775806, 9223372036854775807}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			a, sink := testAgent(t)
			item := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: fmt.Appendf(nil, `[{"childId":"child","attempt":%d,"status":"terminal","terminal":"failed"}]`, pair[0])}}
			feed(t, a, contracts.MuseMethodItemUpdated, item)
			item.Item.Revision = 2
			item.Item.Children = fmt.Appendf(nil, `[{"childId":"child","attempt":%d,"status":"started"}]`, pair[1])
			feed(t, a, contracts.MuseMethodItemUpdated, item)
			row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Len(t, sink.RevivedTasks(), 1)
		})
	}
}

func TestAddressableChildWithoutRunIdentityCannotCloseANewerWorkflowAttempt(t *testing.T) {
	a, sink := testAgent(t)
	a.viewWork.CloseAndWait()
	key := museChildRowKey("session", "child")
	a.sessions["native-child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childKey: key, childID: "child-agent", parentSessionID: "session", subagentID: "child"}
	workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", EntryID: "Native workflow", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":2,"status":"started"}]`)}}
	feed(t, a, contracts.MuseMethodItemUpdated, workflow)
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "addressable", Revision: 1, Kind: museItemSubagent, SubagentID: "child", ChildSessionID: "native-child", Status: "completed", Objective: "Native objective"}})
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Native objective", row.Title)
	assert.Equal(t, "Native workflow", row.GroupLabel)
	assert.Equal(t, "child-agent", row.ChildAgentID)
	assert.Empty(t, sink.RevivedTasks(), "a first active attempt supplies no restart evidence")
	workflow.Item.Revision = 2
	workflow.Item.Children = json.RawMessage(`[{"childId":"child","attempt":2,"status":"terminal","terminal":"failed"}]`)
	feed(t, a, contracts.MuseMethodItemUpdated, workflow)
	row, _ = sink.BackgroundTask(key)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestOlderWorkflowAttemptCannotReplaceTheCurrentNativeFold(t *testing.T) {
	a, sink := testAgent(t)
	a.viewWork.CloseAndWait()
	key := museChildRowKey("session", "child")
	a.sessions["native-child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childKey: key, childID: "child-agent", parentSessionID: "session", subagentID: "child"}
	item := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":2,"status":"started"}]`)}}
	feed(t, a, contracts.MuseMethodItemUpdated, item)
	item.Item.Revision = 2
	item.Item.Children = json.RawMessage(`[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]`)
	feed(t, a, contracts.MuseMethodItemUpdated, item)
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "addressable", Revision: 1, Kind: museItemSubagent, SubagentID: "child", ChildSessionID: "native-child", Status: "completed"}})
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, int64(1), a.sessions["session"].items["workflow"].params.Item.Revision)
	assert.Empty(t, sink.RevivedTasks())
}

func TestWorkflowRejectsANullChildrenFold(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage("null")}})
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, a.sessions["session"].items)
}

func TestInvalidCompletedWorkflowKeepsNativeEvidenceWithoutReplacingOwnership(t *testing.T) {
	for _, children := range []string{"null", `[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]`} {
		t.Run(children, func(t *testing.T) {
			a, sink := testAgent(t)
			item := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":2,"status":"started"}]`)}}
			feed(t, a, contracts.MuseMethodItemUpdated, item)
			item.Item.Revision, item.Item.Status = 2, "completed"
			item.Item.Children = json.RawMessage(children)
			raw := feed(t, a, contracts.MuseMethodItemCompleted, item)
			a.HandleOutput(raw)
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, raw, sink.Messages()[0].Content)
			assert.Equal(t, "session", sink.Messages()[0].AgentSessionID)
			assert.Equal(t, int64(1), a.sessions["session"].items["workflow"].params.Item.Revision)
			row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Empty(t, sink.RevivedTasks())
		})
	}
}
