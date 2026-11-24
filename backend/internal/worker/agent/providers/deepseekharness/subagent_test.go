package deepseekharness

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingChildStore struct {
	*agenttest.Sink
	cause error
	calls int
}

func (s *failingChildStore) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	s.calls++
	return "", s.cause
}

func TestChildCatalogDoesNotCommitBeforeTheChildStoreSucceeds(t *testing.T) {
	t.Parallel()
	const native = `{"version":0,"childId":"native-child","childCreatedAt":1000,"mode":"continuable","label":"Native child"}`
	base := &agenttest.Sink{}
	cause := errors.New("the child store refused the write")
	sink := &failingChildStore{Sink: base, cause: cause}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	stream := a.streams["root"]
	for range 2 {
		require.ErrorIs(t, a.beginChild(stream, []byte(native)), cause)
		assert.NotContains(t, a.childCatalog, "native-child")
		assert.NotContains(t, a.children, "native-child")
	}
	assert.Equal(t, 2, sink.calls)
}

func TestChildCatalogDoesNotCommitBeforeTheNativeFollowSucceeds(t *testing.T) {
	t.Parallel()
	const native = `{"version":0,"childId":"native-child","childCreatedAt":1000,"mode":"one-shot","label":"Native child"}`
	a := newOfflineAgent(t, &agenttest.Sink{})
	stream := a.streams["root"]
	for range 2 {
		require.ErrorContains(t, a.beginChild(stream, []byte(native)), "stream connection")
		assert.NotContains(t, a.childCatalog, "native-child")
		assert.NotContains(t, a.children, "native-child")
	}
}

func TestChildOperationsRefuseUnknownAndOneShotChildrenBeforeTransport(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.children["one-shot"] = &nativeChild{descriptor: nativeChildDescriptor{ID: "one-shot", Parent: "native-root", Mode: "one-shot"}}
	for _, key := range []string{"", "missing", "one-shot"} {
		assert.ErrorIs(t, agenttest.SendChildInput(a, key, agent.ChildInput{Content: "message"}), agent.ErrChildOperationUnsupported)
		assert.ErrorIs(t, a.SteerChildInput(key, "message", nil), agent.ErrChildOperationUnsupported)
		assert.ErrorIs(t, a.InterruptChild(key, agent.StopContext{}), agent.ErrChildOperationUnsupported)
	}
}

func TestContinuableResultAttachesOnlyItsExactFirstSpawnSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Native child"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	require.NoError(t, err)
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID}
	sink.SetSpanType("spawn-first", contracts.DeepseekHarnessToolSubagent)
	sink.SetSpanType("spawn-other", contracts.DeepseekHarnessToolSubagent)
	raw := []byte(`{"turn":1,"message":{"toolCallId":"spawn-first","role":"tool","content":[{"type":"text","text":"started subagent native-child"}],"isError":false}}`)
	require.NoError(t, a.bindChildResult(a.streams["root"], "spawn-first", raw))
	span, err := sink.ChildSpawnSpan(virtualID)
	require.NoError(t, err)
	assert.Equal(t, "spawn-first", span)
	assert.Equal(t, "spawn-first", a.children[descriptor.ID].spawnID)
	require.NoError(t, a.bindChildResult(a.streams["root"], "spawn-first", raw))
	require.Error(t, a.bindChildResult(a.streams["root"], "spawn-other", []byte(`{"turn":1,"message":{"toolCallId":"spawn-other","role":"tool","content":[{"type":"text","text":"started subagent native-child"}],"isError":false}}`)))
	span, err = sink.ChildSpawnSpan(virtualID)
	require.NoError(t, err)
	assert.Equal(t, "spawn-first", span)
}

func TestContinuableResultMatchesNamedCatalogEntry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Native child", Name: "agent-1"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	require.NoError(t, err)
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID}
	sink.SetSpanType("spawn-named", contracts.DeepseekHarnessToolSubagent)
	raw := []byte(`{"turn":1,"message":{"toolCallId":"spawn-named","role":"tool","content":[{"type":"text","text":"started subagent agent-1"}],"isError":false}}`)
	require.NoError(t, a.bindChildResult(a.streams["root"], "spawn-named", raw))
	span, err := sink.ChildSpawnSpan(virtualID)
	require.NoError(t, err)
	assert.Equal(t, "spawn-named", span)
}

func TestChildTurnStatePreservesEachFinalOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		completion agent.MessageCompletion
		status     bgtask.Status
	}{
		{agent.MessageCompletionComplete, bgtask.StatusSucceeded},
		{agent.MessageCompletionInterrupted, bgtask.StatusInterrupted},
		{agent.MessageCompletionError, bgtask.StatusFailed},
		{agent.MessageCompletionFinished, bgtask.StatusEndedWithUnknownOutcome},
	} {
		t.Run(string(tc.completion), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.children["child"] = &nativeChild{active: true}
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))

			require.NoError(t, a.childTurnState("child", false, tc.completion))
			row, present := sink.BackgroundTask("child")
			require.True(t, present)
			assert.Equal(t, tc.status, row.Status)
			assert.False(t, a.ActiveChildTurnState("child").Active)
			assert.False(t, row.EndedAt.IsZero())
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestChildTurnStateRejectsInvalidCompletionBeforeChangingActivity(t *testing.T) {
	t.Parallel()
	for _, completion := range []agent.MessageCompletion{"", "future", " complete "} {
		t.Run(string(completion), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.children["child"] = &nativeChild{active: true}
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning, ActiveForm: "Native activity"}))
			before, present := sink.BackgroundTask("child")
			require.True(t, present)

			require.ErrorContains(t, a.childTurnState("child", false, completion), "invalid child completion")
			after, present := sink.BackgroundTask("child")
			require.True(t, present)
			assert.Equal(t, before, after)
			assert.True(t, a.ActiveChildTurnState("child").Active)
			assert.Equal(t, []bgtask.Status{bgtask.StatusRunning}, sink.BackgroundTaskStatuses("child"))
		})
	}
}

func TestChildTurnStateRevivesAnActiveChildWithoutACompletion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.children["child"] = &nativeChild{descriptor: nativeChildDescriptor{Mode: "continuable"}}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child", Kind: bgtask.KindSubagent, Status: bgtask.StatusEndedWithUnknownOutcome, ActiveForm: "Old activity"}))

	require.NoError(t, a.childTurnState("child", true, ""))
	row, present := sink.BackgroundTask("child")
	require.True(t, present)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
	assert.Empty(t, row.ActiveForm)
	assert.Equal(t, []string{"child"}, sink.RevivedTasks())
	assert.True(t, a.ActiveChildTurnState("child").Active)
	assert.True(t, a.ActiveChildTurnState("child").Steerable)
}

func TestChildTurnStateIgnoresAnUnknownChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	for _, active := range []bool{false, true} {
		require.NoError(t, a.childTurnState("missing", active, ""))
	}
	assert.Empty(t, a.children)
	assert.Empty(t, sink.BackgroundTasks())
}

type failingChildClose struct {
	*agenttest.Sink
	cause  error
	calls  int
	status bgtask.Status
}

func (s *failingChildClose) CloseBackgroundTask(_ string, status bgtask.Status) error {
	s.calls++
	s.status = status
	return s.cause
}

func TestChildTurnStateRetainsARegistryCloseFailure(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	cause := errors.New("the registry refused the close")
	sink := &failingChildClose{Sink: base, cause: cause}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	a.children["child"] = &nativeChild{active: true}
	require.NoError(t, base.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))

	require.ErrorIs(t, a.childTurnState("child", false, agent.MessageCompletionError), cause)
	assert.Equal(t, 1, sink.calls)
	assert.Equal(t, bgtask.StatusFailed, sink.status)
	assert.False(t, a.ActiveChildTurnState("child").Active)
	row, present := base.BackgroundTask("child")
	require.True(t, present)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
}

func TestIdleContinuableChildInterruptClosesPendingToolsAndResetsSpans(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Native child"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode},
	})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: descriptor.ID, Kind: bgtask.KindSubagent, ChildAgentID: virtualID, Title: descriptor.Label, Status: bgtask.StatusRunning}))
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID, active: true}
	childStream := &sessionStream{
		address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "continuable"},
		sessionID:       descriptor.ID,
		parentSessionID: "native-root",
		childAgentID:    virtualID,
		lastSeq:         -1,
		pending:         map[string][]byte{},
		ready:           make(chan struct{}),
	}
	a.streams["child-stream"] = childStream

	require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/start","seq":0,"time":1000,"data":{}}`)))
	require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1001,"data":{"callId":"child-call-1","name":"bash"}}`)))
	require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/end","seq":2,"time":1002,"data":{"reason":{"kind":"completed"}}}`)))
	childSink := sink.Child(virtualID)
	require.False(t, a.ActiveChildTurnState(descriptor.ID).Active)
	require.Equal(t, []agent.TurnState{
		{Active: true, Steerable: true},
		{Active: false, Steerable: false},
	}, childSink.TurnStates(), "continuable child turn start and end must publish SetTurnState")
	seqs := childSink.TurnSeqs()
	require.Len(t, seqs, 2)
	assert.Greater(t, seqs[0], uint64(0))
	assert.Greater(t, seqs[1], seqs[0])
	require.Empty(t, childSink.ClosedSpans())

	require.NoError(t, a.InterruptChild(descriptor.ID, agent.StopContext{}))
	assert.Contains(t, childSink.ClosedSpans(), "child-call-1")
	assert.Contains(t, childSink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, childSink.ProgressUpdates(), agent.ResetProgress())

	var foundClosing bool
	for _, m := range childSink.Messages() {
		if m.Closing && m.SpanID == "child-call-1" {
			foundClosing = true
			assert.Equal(t, agent.MessageCompletionInterrupted, m.Completion)
		}
	}
	assert.True(t, foundClosing, "idle continuable InterruptChild must persist an interrupted closing row for pending tools")
}

func TestContinuableChildTurnStatePublishesMonotonicTransitions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{name: "completed", reason: `{"kind":"completed"}`},
		{name: "error", reason: `{"kind":"error","error":{"message":"boom"}}`},
		{name: "interrupted", reason: `{"kind":"interrupted"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Native child"}
			virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
				RegistryRole:     bgtask.RoleChildTask,
				ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode},
			})
			require.NoError(t, err)
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: descriptor.ID, Kind: bgtask.KindSubagent, ChildAgentID: virtualID, Title: descriptor.Label, Status: bgtask.StatusRunning}))
			a.childCatalog[descriptor.ID] = descriptor
			a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID}
			childStream := &sessionStream{
				address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "continuable"},
				sessionID:       descriptor.ID,
				parentSessionID: "native-root",
				childAgentID:    virtualID,
				lastSeq:         -1,
				pending:         map[string][]byte{},
				ready:           make(chan struct{}),
			}
			a.streams["child-stream"] = childStream

			require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/start","seq":0,"time":1000,"data":{}}`)))
			require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/end","seq":1,"time":1001,"data":{"reason":`+tc.reason+`}}`)))
			require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/start","seq":2,"time":1002,"data":{}}`)))
			require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/end","seq":3,"time":1003,"data":{"reason":{"kind":"completed"}}}`)))

			childSink := sink.Child(virtualID)
			assert.Equal(t, []agent.TurnState{
				{Active: true, Steerable: true},
				{Active: false, Steerable: false},
				{Active: true, Steerable: true},
				{Active: false, Steerable: false},
			}, childSink.TurnStates())
			seqs := childSink.TurnSeqs()
			require.Len(t, seqs, 4)
			for i := 1; i < len(seqs); i++ {
				assert.Greater(t, seqs[i], seqs[i-1], "child turn sequence must increase monotonically")
			}
		})
	}

	t.Run("one_shot_child_does_not_publish_turn_state", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		descriptor := nativeChildDescriptor{ID: "one-shot-child", Parent: "native-root", Mode: "one-shot", Label: "One-shot"}
		virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole:     bgtask.RoleChildTask,
			ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode},
		})
		require.NoError(t, err)
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: descriptor.ID, Kind: bgtask.KindSubagent, ChildAgentID: virtualID, Title: descriptor.Label, Status: bgtask.StatusRunning}))
		a.childCatalog[descriptor.ID] = descriptor
		a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID}
		childStream := &sessionStream{
			address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "one-shot"},
			sessionID:       descriptor.ID,
			parentSessionID: "native-root",
			childAgentID:    virtualID,
			lastSeq:         -1,
			pending:         map[string][]byte{},
			ready:           make(chan struct{}),
		}
		a.streams["one-shot-stream"] = childStream

		require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/start","seq":0,"time":1000,"data":{}}`)))
		require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"turn/end","seq":1,"time":1001,"data":{"reason":{"kind":"completed"}}}`)))
		assert.Empty(t, sink.Child(virtualID).TurnStates(), "one-shot child must remain registry-only")
	})
}
func TestWorkflowEndClosesRemainingChildren(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	root := a.streams["root"]
	a.children["child-ended"] = &nativeChild{agentID: "stored-ended", active: true}
	a.children["child-open"] = &nativeChild{agentID: "stored-open", active: true}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "child-ended", Kind: bgtask.KindSubagent, ChildAgentID: "stored-ended", Status: bgtask.StatusRunning}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "child-open", Kind: bgtask.KindSubagent, ChildAgentID: "stored-open", Status: bgtask.StatusRunning}))

	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowStart, []byte(`{"runId":"wf-1","name":"Audit"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentStart, []byte(`{"runId":"wf-1","childId":"child-ended","seq":1,"label":"Step 1"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentStart, []byte(`{"runId":"wf-1","childId":"child-open","seq":2,"label":"Step 2"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentEnd, []byte(`{"runId":"wf-1","seq":1,"outcome":"completed"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowEnd, []byte(`{"runId":"wf-1","stopReason":"failed"}`)))

	endedRow, ok := sink.BackgroundTask("child-ended")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, endedRow.Status)

	openRow, ok := sink.BackgroundTask("child-open")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, openRow.Status, "workflow run-end must close remaining open children")
	assert.False(t, a.ActiveChildTurnState("child-open").Active)

	wfRow, ok := sink.BackgroundTask("deepseek-workflow:wf-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, wfRow.Status)
}

func TestOutOfOrderWorkflowAgentStartAttachesWhenChildArrives(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	connection, opens := serveFollows(t, func(_ int, streamID string) map[string]any { return emptySnapshot(streamID) })
	a := newOfflineAgent(t, sink)
	a.conn = connection
	root := a.streams["root"]

	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowStart, []byte(`{"runId":"wf-early","name":"Plan"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentStart, []byte(`{"runId":"wf-early","childId":"child-late","seq":1,"label":"Draft"}`)), "early workflow agent start must not return a stream-fatal error")
	require.NoError(t, a.beginChild(root, []byte(`{"version":0,"childId":"child-late","childCreatedAt":1000,"mode":"one-shot","label":"Fallback"}`)))
	<-opens

	row, ok := sink.BackgroundTask("child-late")
	require.True(t, ok)
	assert.Equal(t, "deepseek-workflow:wf-early", row.GroupKey)
	assert.Equal(t, "Plan", row.GroupLabel)
	assert.Equal(t, "Draft", row.Title)
}

func TestBeginOwnedChildAcceptsExternalAndUnknownModesWithoutCrashing(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"external", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			raw := []byte(`{"version":0,"childId":"child-` + mode + `","childCreatedAt":1000,"mode":"` + mode + `","label":"Non-owned"}`)
			require.NoError(t, a.beginChild(a.streams["root"], raw))
			assert.Empty(t, sink.BackgroundTasks())
		})
	}
}

func TestBindChildResultMatchesUniqueNamedChildAndDoesNotCrashWhenUnmatched(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "child-uuid-1", Parent: "native-root", Mode: "continuable", Label: "reviewer"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID,
		Title:            descriptor.Label,
		Options:          optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode},
	})
	require.NoError(t, err)
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: virtualID}
	sink.SetSpanType("spawn-named", contracts.DeepseekHarnessToolSubagent)
	sink.SetSpanType("spawn-missing", contracts.DeepseekHarnessToolSubagent)

	namedRaw := []byte(`{"turn":1,"message":{"toolCallId":"spawn-named","role":"tool","content":[{"type":"text","text":"started subagent reviewer"}],"isError":false}}`)
	require.NoError(t, a.bindChildResult(a.streams["root"], "spawn-named", namedRaw))
	span, err := sink.ChildSpawnSpan(virtualID)
	require.NoError(t, err)
	assert.Equal(t, "spawn-named", span)
	assert.Equal(t, "spawn-named", a.children[descriptor.ID].spawnID)

	missingRaw := []byte(`{"turn":1,"message":{"toolCallId":"spawn-missing","role":"tool","content":[{"type":"text","text":"started subagent nonexistent"}],"isError":false}}`)
	require.NoError(t, a.bindChildResult(a.streams["root"], "spawn-missing", missingRaw), "unmatched child result must warn instead of crashing the stream")
}

type selectiveCloseFailSink struct {
	*agenttest.Sink
	failKey string
	cause   error
}

func (s *selectiveCloseFailSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if rowKey == s.failKey && s.cause != nil {
		return s.cause
	}
	return s.Sink.CloseBackgroundTask(rowKey, status)
}

func TestWorkflowEndClosesAllChildrenAndWorkflowRowBeforeDeletingRun(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	cause := errors.New("close child-a refused")
	sink := &selectiveCloseFailSink{Sink: base, failKey: "child-a", cause: cause}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	root := a.streams["root"]
	a.children["child-a"] = &nativeChild{agentID: "stored-a", active: true}
	a.children["child-b"] = &nativeChild{agentID: "stored-b", active: true}
	require.NoError(t, base.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "child-a", Kind: bgtask.KindSubagent, ChildAgentID: "stored-a", Status: bgtask.StatusRunning}))
	require.NoError(t, base.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "child-b", Kind: bgtask.KindSubagent, ChildAgentID: "stored-b", Status: bgtask.StatusRunning}))

	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowStart, []byte(`{"runId":"wf-err","name":"Audit"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentStart, []byte(`{"runId":"wf-err","childId":"child-a","seq":1,"label":"Step 1"}`)))
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowAgentStart, []byte(`{"runId":"wf-err","childId":"child-b","seq":2,"label":"Step 2"}`)))

	endPayload := []byte(`{"runId":"wf-err","stopReason":"failed"}`)
	require.ErrorIs(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowEnd, endPayload), cause)

	// One child's failed close must not strand sibling children or the workflow row.
	rowB, ok := base.BackgroundTask("child-b")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, rowB.Status)
	wfRow, ok := base.BackgroundTask("deepseek-workflow:wf-err")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, wfRow.Status)

	// The failed workflow must stay tracked so a retry closes the remaining child cleanly.
	sink.cause = nil
	require.NoError(t, a.workflowEvent(root, contracts.DeepseekHarnessEventWorkflowEnd, endPayload))
	rowA, ok := base.BackgroundTask("child-a")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, rowA.Status)
	assert.False(t, a.ActiveChildTurnState("child-a").Active)
	a.Mu.Lock()
	_, stillTracked := a.workflows["wf-err"]
	a.Mu.Unlock()
	assert.False(t, stillTracked)
}

func TestInterruptChildUsesUserRouteAndFallsBackOnNotFound(t *testing.T) {
	t.Parallel()
	t.Run("prefers subagents/interruptByUser", func(t *testing.T) {
		t.Parallel()
		var paths []string
		var gotArgs map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			var req struct {
				ID      string `json:"rpcId"`
				Payload struct {
					Args map[string]any `json:"args"`
				} `json:"payload"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			gotArgs = req.Payload.Args
			if r.URL.Path != "/api/subagents/interruptByUser" {
				http.NotFound(w, r)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		a := newOfflineAgent(t, &agenttest.Sink{})
		a.rpc = remoteRPC{endpoint: endpoint}
		a.children["child-1"] = &nativeChild{descriptor: nativeChildDescriptor{ID: "child-1", Parent: "native-root", Mode: "continuable"}, active: true}

		require.NoError(t, a.InterruptChild("child-1", agent.StopContext{}))
		assert.Equal(t, []string{"/api/subagents/interruptByUser"}, paths)
		assert.Equal(t, map[string]any{"childSessionId": "child-1"}, gotArgs)
	})

	t.Run("falls back to subagents/interruptByParent on 404", func(t *testing.T) {
		t.Parallel()
		var paths []string
		var fallbackArgs map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			var req struct {
				ID      string `json:"rpcId"`
				Payload struct {
					Args map[string]any `json:"args"`
				} `json:"payload"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			if r.URL.Path == "/api/subagents/interruptByUser" {
				http.NotFound(w, r)
				return
			}
			fallbackArgs = req.Payload.Args
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		a := newOfflineAgent(t, &agenttest.Sink{})
		a.rpc = remoteRPC{endpoint: endpoint}
		a.children["child-1"] = &nativeChild{descriptor: nativeChildDescriptor{ID: "child-1", Parent: "native-root", Mode: "continuable"}, active: true}

		require.NoError(t, a.InterruptChild("child-1", agent.StopContext{}))
		assert.Equal(t, []string{"/api/subagents/interruptByUser", "/api/subagents/interruptByParent"}, paths)
		assert.Equal(t, map[string]any{"childSessionId": "child-1", "parentSessionId": "native-root", "mode": "continuable"}, fallbackArgs)
	})
}
