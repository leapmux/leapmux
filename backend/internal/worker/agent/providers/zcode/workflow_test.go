package zcode

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// zcodeTestPollInterval is how often a test reads a state that a goroutine sets.
const zcodeTestPollInterval = 2 * time.Millisecond

// zcodeTestRunID is the run of the test workflow.
const zcodeTestRunID = "dwfrun-1"

// zcodeTestWorkflowLabel labels the test workflow.
const zcodeTestWorkflowLabel = "leapmux-flow-probe"

// zcodeWorkflowTaskLine is the background task of the test run, as ZCode 3.16.3
// states it.
func zcodeWorkflowTaskLine(t *testing.T, seq int64, status string) []byte {
	t.Helper()
	return zcodeEventLine(t, seq, contracts.ZCodeEventSessionUpdated, fmt.Sprintf(
		`{"taskId":%q,"toolCallId":"create-flow","toolName":"CreateWorkflow","taskKind":"workflow","cancellable":true,"description":%q,"status":%q}`,
		zcodeTestRunID, zcodeTestWorkflowLabel, status))
}

// zcodeTestNode is one node of the test run.
func zcodeTestNode(siteID, kind, actorSiteID, phase, outcome string) map[string]any {
	node := map[string]any{"siteId": siteID, "ordinal": 1, "kind": kind, "phase": phase, "instructionsHead": "Task of " + siteID}
	if actorSiteID != "" {
		node["actorSiteId"], node["actorOrdinal"] = actorSiteID, 1
	}
	if outcome != "" {
		node["outcome"] = outcome
	}
	return node
}

// zcodeTestActors are the two named subagents of the test run.
var zcodeTestActors = []map[string]any{
	{"siteId": "actor#1", "ordinal": 1, "name": "alpha", "sessionId": "sess_dwf-1-actor_1_1", "status": "running"},
	{"siteId": "actor#2", "ordinal": 1, "name": "beta", "sessionId": "sess_dwf-1-actor_2_1", "status": "running"},
}

// zcodeWorkflowSnapshot is a snapshot frame that states the test run.
func zcodeWorkflowSnapshot(status string, actors []map[string]any, nodes ...map[string]any) map[string]any {
	return map[string]any{
		"topic": zcodeTestTopic, "subscriptionId": "sub-1",
		"payload": map[string]any{"kind": "snapshot", "snapshot": map[string]any{
			"sessionId": "sess-1", "pendingInteractions": []any{},
			"workflowRuns": map[string]any{"revision": 1, "runs": []map[string]any{{
				"runId": zcodeTestRunID, "toolCallId": "create-flow", "status": status, "actors": actors, "nodes": nodes,
			}}},
		}},
	}
}

// zcodeWorkflowDelta is a delta frame of one workflowRun.updated change.
func zcodeWorkflowDelta(status string, actors []map[string]any, nodes ...map[string]any) map[string]any {
	change := map[string]any{"op": "workflowRun.updated", "runId": zcodeTestRunID, "revision": 2, "actors": actors, "nodes": nodes}
	if status != "" {
		change["run"] = map[string]any{"status": status}
	}
	return map[string]any{
		"topic": zcodeTestTopic, "subscriptionId": "sub-1",
		"payload": map[string]any{"kind": "deltas", "deltas": []map[string]any{change}},
	}
}

// zcodeWorkflowTestRow is the running workflow row that a background task opens.
func zcodeWorkflowTestRow(rowKey, label string) bgtask.Upsert {
	return bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindWorkflow, GroupKey: rowKey, GroupLabel: label,
		Title: label, Description: label, Status: bgtask.StatusRunning,
	}
}

// newZCodeWorkflowRuns tracks the test run.
func newZCodeWorkflowRuns(t *testing.T) *zcodeWorkflowRuns {
	t.Helper()
	runs := &zcodeWorkflowRuns{}
	require.True(t, runs.track(zcodeTestRunID, zcodeWorkflowTestRow("create-flow", zcodeTestWorkflowLabel)))
	return runs
}

// --- the tracker ---

// Each ask node becomes a row in the run row's group, titled by its subagent. A
// world-read node is no agent's work and gets no row.
func TestZCodeWorkflowRunsDrawEachAskNode(t *testing.T) {
	t.Parallel()
	runs := newZCodeWorkflowRuns(t)
	upserts, ended := runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""),
		zcodeTestNode("ask#2", "ask", "actor#2", "dispatched", ""),
		zcodeTestNode("read#1", "world-read", "", "executing", ""),
	)))
	assert.False(t, ended)
	require.Len(t, upserts, 2)
	assert.Equal(t, []string{"create-flow/ask#1#1", "create-flow/ask#2#1"}, []string{upserts[0].RowKey, upserts[1].RowKey},
		"the rows follow the order of the nodes")
	assert.Equal(t, bgtask.Upsert{
		RowKey: "create-flow/ask#1#1", Kind: bgtask.KindSubagent, GroupKey: "create-flow", GroupLabel: zcodeTestWorkflowLabel,
		Title: "alpha", Description: "Task of ask#1", Status: bgtask.StatusRunning,
	}, upserts[0])
	assert.Equal(t, "beta", upserts[1].Title)
	assert.Equal(t, bgtask.StatusPending, upserts[1].Status)
	assert.True(t, runs.active())
}

// A change states only what changed: a node that did not change writes nothing,
// and a blank field keeps the node's value. The run's end ends the tracking.
func TestZCodeWorkflowRunsApplyChanges(t *testing.T) {
	t.Parallel()
	runs := newZCodeWorkflowRuns(t)
	runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""),
		zcodeTestNode("ask#2", "ask", "actor#2", "executing", ""),
	)))

	upserts, ended := runs.apply(zcodeLogicalFrame(t, zcodeWorkflowDelta("", nil,
		map[string]any{"siteId": "ask#1", "ordinal": 1, "phase": "settled", "outcome": "ok"})))
	assert.False(t, ended)
	require.Len(t, upserts, 1, "only the changed node writes a row")
	assert.Equal(t, "alpha", upserts[0].Title, "the actor of the node stays")
	assert.Equal(t, "Task of ask#1", upserts[0].Description, "the instructions stay")
	assert.Equal(t, bgtask.StatusSucceeded, upserts[0].Status)

	upserts, ended = runs.apply(zcodeLogicalFrame(t, zcodeWorkflowDelta("completed", nil,
		map[string]any{"siteId": "ask#2", "ordinal": 1, "phase": "settled", "outcome": "failed"})))
	assert.True(t, ended)
	require.Len(t, upserts, 2)
	assert.Equal(t, "create-flow", upserts[0].RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, upserts[0].Status)
	assert.Equal(t, "create-flow/ask#2#1", upserts[1].RowKey)
	assert.Equal(t, bgtask.StatusFailed, upserts[1].Status)
	assert.False(t, runs.active())

	upserts, ended = runs.apply(zcodeLogicalFrame(t, zcodeWorkflowDelta("completed", nil)))
	assert.False(t, ended, "a run ends once")
	assert.Empty(t, upserts)
}

// A node that did not settle before its run ended has no outcome to state.
func TestZCodeWorkflowRunsEndTheUnsettledNodesOfAnEndedRun(t *testing.T) {
	t.Parallel()
	runs := newZCodeWorkflowRuns(t)
	runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""))))
	upserts, ended := runs.apply(zcodeLogicalFrame(t, zcodeWorkflowDelta("cancelled", nil)))
	assert.True(t, ended)
	require.Len(t, upserts, 2)
	assert.Equal(t, "create-flow", upserts[0].RowKey)
	assert.Equal(t, bgtask.StatusStopped, upserts[0].Status)
	assert.Equal(t, "create-flow/ask#1#1", upserts[1].RowKey)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[1].Status)
}

// A removed run ends its own row and every remaining ask row. A removed node
// writes no more rows.
func TestZCodeWorkflowRunsRemoval(t *testing.T) {
	t.Parallel()
	runs := newZCodeWorkflowRuns(t)
	runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""),
		zcodeTestNode("ask#2", "ask", "actor#2", "executing", ""))))

	frame := zcodeWorkflowDelta("", nil)
	frame["payload"].(map[string]any)["deltas"].([]map[string]any)[0]["removedNodes"] = []map[string]any{{"siteId": "ask#2", "ordinal": 1}}
	upserts, _ := runs.apply(zcodeLogicalFrame(t, frame))
	assert.Empty(t, upserts)
	upserts, ended := runs.apply(zcodeLogicalFrame(t, map[string]any{"payload": map[string]any{"kind": "deltas", "deltas": []map[string]any{
		{"op": "workflowRun.removed", "runId": zcodeTestRunID, "revision": 3},
	}}}))
	assert.True(t, ended)
	require.Len(t, upserts, 2, "the removed node writes nothing")
	assert.Equal(t, "create-flow", upserts[0].RowKey)
	assert.Equal(t, bgtask.KindWorkflow, upserts[0].Kind)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[0].Status)
	assert.Equal(t, "create-flow/ask#1#1", upserts[1].RowKey)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[1].Status)
}

// A background result that arrived first stays authoritative. Removal closes
// only the remaining node, and teardown writes nothing for the ended run.
func TestZCodeWorkflowRunsRemovalPreservesBackgroundResult(t *testing.T) {
	t.Parallel()
	runs := newZCodeWorkflowRuns(t)
	runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""))))
	completed := zcodeWorkflowTestRow("create-flow", zcodeTestWorkflowLabel)
	completed.Status = bgtask.StatusSucceeded
	assert.False(t, runs.track(zcodeTestRunID, completed))

	upserts, ended := runs.apply(zcodeLogicalFrame(t, map[string]any{"payload": map[string]any{"kind": "deltas", "deltas": []map[string]any{
		{"op": "workflowRun.removed", "runId": zcodeTestRunID, "revision": 3},
	}}}))
	assert.True(t, ended)
	require.Len(t, upserts, 1, "the workflow's completed row needs no second final upsert")
	assert.Equal(t, "create-flow/ask#1#1", upserts[0].RowKey)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[0].Status)
	assert.Empty(t, runs.clear(bgtask.StatusStopped))
}

// A run that no background task stated has no row to group under, and an
// unnamed subagent's row takes its title from the instructions.
func TestZCodeWorkflowRunsTitlesAndUntrackedRuns(t *testing.T) {
	t.Parallel()
	untracked := &zcodeWorkflowRuns{}
	upserts, ended := untracked.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""))))
	assert.Empty(t, upserts)
	assert.False(t, ended)
	assert.False(t, untracked.track("", zcodeWorkflowTestRow("create-flow", "probe")))
	assert.False(t, untracked.track("run", zcodeWorkflowTestRow("", "probe")))

	runs := newZCodeWorkflowRuns(t)
	assert.False(t, runs.track(zcodeTestRunID, zcodeWorkflowTestRow("create-flow", "")), "a run is tracked once")
	upserts, _ = runs.apply(zcodeLogicalFrame(t, zcodeWorkflowSnapshot("running", nil,
		zcodeTestNode("ask#1", "ask", "actor#9", "executing", ""))))
	require.Len(t, upserts, 1)
	assert.Equal(t, "Task of ask#1", upserts[0].Title)
	assert.Equal(t, zcodeTestWorkflowLabel, upserts[0].GroupLabel, "a blank label keeps the run's label")
}

// A snapshot lists every run of the session, so a tracked run that it does not
// list ends: the frames will never report it. Its final upserts close its own
// row and each ask row that remained active. A run that a later background task
// states is not in an earlier snapshot, and stays tracked.
func TestZCodeWorkflowRunsEndARunThatTheSnapshotDoesNotList(t *testing.T) {
	t.Parallel()
	for name, snapshot := range map[string]map[string]any{
		"a snapshot without the run": zcodeWorkflowSnapshot("running", nil),
		"a snapshot without runs":    {"payload": map[string]any{"kind": "snapshot", "snapshot": map[string]any{"sessionId": "sess-1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runs := &zcodeWorkflowRuns{}
			require.True(t, runs.track("another-run", zcodeWorkflowTestRow("create-other", "other")))
			seed := zcodeWorkflowDelta("running", zcodeTestActors, zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""))
			seed["payload"].(map[string]any)["deltas"].([]map[string]any)[0]["runId"] = "another-run"
			initial, _ := runs.apply(zcodeLogicalFrame(t, seed))
			require.Len(t, initial, 1)
			assert.Equal(t, bgtask.StatusRunning, initial[0].Status)

			upserts, ended := runs.apply(zcodeLogicalFrame(t, snapshot))
			assert.True(t, ended)
			assert.False(t, runs.active())
			require.Len(t, upserts, 2)
			assert.Equal(t, "create-other", upserts[0].RowKey)
			assert.Equal(t, bgtask.KindWorkflow, upserts[0].Kind)
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[0].Status)
			assert.Equal(t, "create-other/ask#1#1", upserts[1].RowKey)
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, upserts[1].Status)

			require.True(t, runs.track(zcodeTestRunID, zcodeWorkflowTestRow("create-flow", zcodeTestWorkflowLabel)))
			_, ended = runs.apply(zcodeLogicalFrame(t, zcodeWorkflowDelta("", nil)))
			assert.False(t, ended)
			assert.True(t, runs.active(), "a delta frame ends no run that it does not state")
		})
	}
}

func TestZCodeWorkflowNodeStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase, outcome string
		ended          bool
		want           bgtask.Status
	}{
		{"queued", "", false, bgtask.StatusPending},
		{"dispatched", "", false, bgtask.StatusPending},
		{"executing", "", false, bgtask.StatusRunning},
		{"waiting", "", false, bgtask.StatusRunning},
		{"repairing", "", false, bgtask.StatusRunning},
		{"nudged", "", false, bgtask.StatusRunning},
		{"settled", "ok", false, bgtask.StatusSucceeded},
		{"settled", "failed", false, bgtask.StatusFailed},
		{"settled", "cancelled", false, bgtask.StatusStopped},
		{"settled", "", false, bgtask.StatusEndedWithUnknownOutcome},
		{"settled", "ok", true, bgtask.StatusSucceeded},
		{"executing", "", true, bgtask.StatusEndedWithUnknownOutcome},
		{"queued", "", true, bgtask.StatusEndedWithUnknownOutcome},
	} {
		assert.Equal(t, tc.want, zcodeWorkflowNodeStatus(zcodeWorkflowNodeState{Phase: tc.phase, Outcome: tc.outcome}, tc.ended),
			"%s/%s ended=%v", tc.phase, tc.outcome, tc.ended)
	}
}

// --- the agent ---

// answerZCodeSubscribe answers the next subscription request.
func answerZCodeSubscribe(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, n int) zcodeSentRequest {
	t.Helper()
	request := waitZCodeRequestNumber(t, stdin, MethodConversationSubscribe, n)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request),
		json.RawMessage(`{"ack":{"subscriptionId":"sub-1","mode":"snapshot","logEpoch":"epoch-1"}}`)))
	return request
}

// zcodeRequestCount counts the requests for method that stdin holds.
func zcodeRequestCount(t *testing.T, stdin *zcodeRecordedStdin, method string) int {
	t.Helper()
	count := 0
	for _, request := range stdin.Requests(t) {
		if request.Method == method {
			count++
		}
	}
	return count
}

// zcodeConversationOpen reports the topic and the ID of the open subscription.
func zcodeConversationOpen(a *Agent) (string, string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.conversation.topic, a.conversation.subscriptionID
}

// A workflow run's row heads its group, and the run opens the subscription,
// which draws a row for each subagent's task. The subscription stays open past
// the background task's end, until the frames report the run's own end.
func TestZCodeWorkflowRunGroupsItsSubagents(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	a.HandleOutput(zcodeWorkflowTaskLine(t, 1, "running"))
	row, ok := sink.BackgroundTask("create-flow")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, row.Kind)
	assert.Equal(t, "create-flow", row.GroupKey)
	assert.Equal(t, zcodeTestWorkflowLabel, row.GroupLabel)

	subscribe := answerZCodeSubscribe(t, a, stdin, 0)
	assert.JSONEq(t, `{"topic":"conversation/sess-1","connectionId":"leapmux","clientMode":"web-remote-replayable","workflowRunDeltas":true}`, string(subscribe.Params))
	require.Eventually(t, func() bool { _, id := zcodeConversationOpen(a); return id == "sub-1" }, zcodeTestRPCTimeout, zcodeTestPollInterval)

	a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""),
		zcodeTestNode("ask#2", "ask", "actor#2", "executing", ""))))
	alpha, ok := sink.BackgroundTask("create-flow/ask#1#1")
	require.True(t, ok)
	assert.Equal(t, "alpha", alpha.Title)
	assert.Equal(t, "create-flow", alpha.GroupKey)
	assert.Equal(t, bgtask.StatusRunning, alpha.Status)

	// The background task ends before the frame that settles the last node.
	a.HandleOutput(zcodeWorkflowTaskLine(t, 2, "completed"))
	topic, _ := zcodeConversationOpen(a)
	assert.Equal(t, zcodeTestTopic, topic, "the subscription waits for the frames to end the run")

	a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeWorkflowDelta("completed", nil,
		map[string]any{"siteId": "ask#1", "ordinal": 1, "phase": "settled", "outcome": "ok"},
		map[string]any{"siteId": "ask#2", "ordinal": 1, "phase": "settled", "outcome": "ok"})))
	unsubscribe := waitZCodeRequestNumber(t, stdin, MethodConversationUnsubscribe, 0)
	assert.JSONEq(t, `{"topic":"conversation/sess-1","subscriptionId":"sub-1","connectionId":"leapmux"}`, string(unsubscribe.Params))
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, unsubscribe), json.RawMessage(`{}`)))
	for _, key := range []string{"create-flow/ask#1#1", "create-flow/ask#2#1"} {
		node, ok := sink.BackgroundTask(key)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, node.Status)
	}
	finished, _ := sink.BackgroundTask("create-flow")
	assert.Equal(t, bgtask.StatusSucceeded, finished.Status)
	require.Eventually(t, func() bool { topic, _ := zcodeConversationOpen(a); return topic == "" }, zcodeTestRPCTimeout, zcodeTestPollInterval)
	assert.Equal(t, 1, zcodeRequestCount(t, stdin, MethodConversationSubscribe), "a repeated background task opens nothing more")
}

// newZCodeActiveWorkflowAgent opens one workflow and one running ask row.
func newZCodeActiveWorkflowAgent(t *testing.T) (*Agent, *agenttest.Sink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeWorkflowTaskLine(t, 1, "running"))
	answerZCodeSubscribe(t, a, stdin, 0)
	require.Eventually(t, func() bool { _, id := zcodeConversationOpen(a); return id == "sub-1" }, zcodeTestRPCTimeout, zcodeTestPollInterval)
	a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeWorkflowSnapshot("running", zcodeTestActors,
		zcodeTestNode("ask#1", "ask", "actor#1", "executing", ""))))
	for _, key := range []string{"create-flow", "create-flow/ask#1#1"} {
		row, ok := sink.BackgroundTask(key)
		require.True(t, ok)
		require.Equal(t, bgtask.StatusRunning, row.Status)
	}
	return a, sink, stdin
}

// Replacing the session ends its workflow rows as interrupted, even when no
// terminal background event or conversation frame arrives.
func TestZCodeClearContextEndsActiveWorkflowRows(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeActiveWorkflowAgent(t)
	answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"sess-2"},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodConversationUnsubscribe, `{}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{}`)

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "sess-2", sessionID)
	for _, key := range []string{"create-flow", "create-flow/ask#1#1"} {
		row, ok := sink.BackgroundTask(key)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, row.Status, key)
	}
	assert.False(t, a.workflows.active())
}

// Process teardown closes the rows through forgetZCodeConversation. An
// intentional stop interrupts them; an unexpected exit fails them.
func TestZCodeProcessTeardownEndsActiveWorkflowRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, a *Agent)
		want bgtask.Status
	}{
		{"stop", func(t *testing.T, a *Agent) {
			a.SimulateExitForTest()
			a.Stop()
		}, bgtask.StatusStopped},
		{"wait", func(t *testing.T, a *Agent) {
			a.SimulateExitForTest()
			require.NoError(t, a.Wait())
		}, bgtask.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newZCodeActiveWorkflowAgent(t)
			tc.run(t, a)
			for _, key := range []string{"create-flow", "create-flow/ask#1#1"} {
				row, ok := sink.BackgroundTask(key)
				require.True(t, ok)
				assert.Equal(t, tc.want, row.Status, key)
			}
			assert.False(t, a.workflows.active())
		})
	}
}

// A run and a hook trust share one subscription: the trust neither opens a
// second one nor closes the run's.
func TestZCodeWorkflowRunSharesTheSubscriptionWithATrust(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeTrustAgent(t, 1)
	a.HandleOutput(zcodeWorkflowTaskLine(t, 5, "running"))
	answerZCodeSubscribe(t, a, stdin, 0)
	require.Eventually(t, func() bool { _, id := zcodeConversationOpen(a); return id == "sub-1" }, zcodeTestRPCTimeout, zcodeTestPollInterval)

	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"workspace-hook-0-UserPromptSubmit-0-0", "pending_trust"})
	go func() {
		ackZCodeHookCommand(t, a, waitZCodeRequestNumber(t, stdin, MethodCommand, 0), zcodeCommandAccepted)
		a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(review)))
		ackZCodeHookCommand(t, a, waitZCodeRequestNumber(t, stdin, MethodCommand, 1), zcodeCommandAccepted)
	}()
	require.NoError(t, zcodeWorkspaceTrustHost{a}.TrustWorkspace())
	topic, _ := zcodeConversationOpen(a)
	assert.Equal(t, zcodeTestTopic, topic, "the run still needs the subscription")
	assert.Zero(t, zcodeRequestCount(t, stdin, MethodConversationUnsubscribe))
	assert.Equal(t, 1, zcodeRequestCount(t, stdin, MethodConversationSubscribe))
	assert.True(t, zcodeHooksTrusted(t, a))
}

// A subscription that ZCode refuses leaves none open, and the next need tries
// again.
func TestZCodeConversationRetriesARefusedSubscription(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.workflows.track(zcodeTestRunID, zcodeWorkflowTestRow("create-flow", zcodeTestWorkflowLabel))
	a.Mu.Unlock()
	refuseZCodeRequest(t, a, stdin, MethodConversationSubscribe, -32601, "method not found")
	require.ErrorContains(t, a.reconcileZCodeConversation(), "subscribe to the ZCode conversation")
	topic, _ := zcodeConversationOpen(a)
	assert.Empty(t, topic)

	go answerZCodeSubscribe(t, a, stdin, 1)
	require.NoError(t, a.reconcileZCodeConversation())
	topic, id := zcodeConversationOpen(a)
	assert.Equal(t, zcodeTestTopic, topic)
	assert.Equal(t, "sub-1", id)
}

// A new session closes the subscription of the session it replaced, whose runs
// went with it, and a stopped agent subscribes to nothing.
func TestZCodeConversationFollowsTheSession(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.workflows.track(zcodeTestRunID, zcodeWorkflowTestRow("create-flow", zcodeTestWorkflowLabel))
	a.Mu.Unlock()
	go answerZCodeSubscribe(t, a, stdin, 0)
	require.NoError(t, a.reconcileZCodeConversation())

	a.Mu.Lock()
	a.sessionID = "sess-2"
	a.workflows.clear(bgtask.StatusStopped)
	a.Mu.Unlock()
	go func() {
		request := waitZCodeRequestNumber(t, stdin, MethodConversationUnsubscribe, 0)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
	}()
	require.NoError(t, a.reconcileZCodeConversation())
	topic, _ := zcodeConversationOpen(a)
	assert.Empty(t, topic)
	unsubscribe := waitZCodeRequestNumber(t, stdin, MethodConversationUnsubscribe, 0)
	assert.Contains(t, string(unsubscribe.Params), `"topic":"conversation/sess-1"`)

	a.Mu.Lock()
	a.workflows.track("run-2", zcodeWorkflowTestRow("create-other", "other"))
	a.Mu.Unlock()
	a.SetStoppedForTest(true)
	before := len(stdin.Requests(t))
	require.NoError(t, a.reconcileZCodeConversation())
	assert.Len(t, stdin.Requests(t), before, "a stopped agent subscribes to nothing")
}
