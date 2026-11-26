package deepseekharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
		_, err := a.SteerChildInput(key, agent.SteerRequest{Content: "message"})
		assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
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
	a.registerCatalogChildLocked(descriptor)
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
	a.registerCatalogChildLocked(descriptor)
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
		var mu sync.Mutex
		var paths []string
		var gotArgs map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID      string `json:"rpcId"`
				Payload struct {
					Args map[string]any `json:"args"`
				} `json:"payload"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			mu.Lock()
			paths = append(paths, r.URL.Path)
			gotArgs = req.Payload.Args
			mu.Unlock()
			if r.URL.Path != "/api/subagents/interruptByUser" {
				http.NotFound(w, r)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
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
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"/api/subagents/interruptByUser"}, paths)
		assert.Equal(t, map[string]any{"childSessionId": "child-1"}, gotArgs)
	})

	t.Run("falls back to subagents/interruptByParent on 404", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var paths []string
		var fallbackArgs map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID      string `json:"rpcId"`
				Payload struct {
					Args map[string]any `json:"args"`
				} `json:"payload"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			if r.URL.Path == "/api/subagents/interruptByUser" {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			fallbackArgs = req.Payload.Args
			mu.Unlock()
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
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
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"/api/subagents/interruptByUser", "/api/subagents/interruptByParent"}, paths)
		assert.Equal(t, map[string]any{"childSessionId": "child-1", "parentSessionId": "native-root", "mode": "continuable"}, fallbackArgs)
	})
}

func TestSendChildInputSerializesConcurrentCallsAndClearsActiveOnUnconfirmedFailure(t *testing.T) {
	t.Parallel()

	t.Run("concurrent SendChildInput allows only one request", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}), make(chan struct{})
		var enteredOnce sync.Once
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			calls.Add(1)
			enteredOnce.Do(func() { close(entered) })
			<-release
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"messageId": "msg-1"}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}
		a.children["child-1"] = &nativeChild{
			descriptor: nativeChildDescriptor{ID: "child-1", Parent: "native-root", Mode: "continuable"},
			agentID:    "stored-child-1",
		}

		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)
		go func() {
			_, err := a.SendChildInput("child-1", agent.ChildInput{Content: "first"})
			firstDone <- err
		}()
		<-entered
		go func() {
			_, err := a.SendChildInput("child-1", agent.ChildInput{Content: "second"})
			secondDone <- err
		}()
		close(release)
		require.NoError(t, <-firstDone)
		require.ErrorIs(t, <-secondDone, agent.ErrAgentBusy)
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("empty messageId and HTTP 502 clear optimistic child active state", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			switch calls.Add(1) {
			case 1:
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": req.ID,
					"result": map[string]any{"ok": true, "value": map[string]any{"messageId": ""}},
				}))
			case 2:
				w.WriteHeader(http.StatusBadGateway)
			default:
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": req.ID,
					"result": map[string]any{"ok": true, "value": map[string]any{"messageId": "msg-ok"}},
				}))
			}
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}
		a.children["child-1"] = &nativeChild{
			descriptor: nativeChildDescriptor{ID: "child-1", Parent: "native-root", Mode: "continuable"},
			agentID:    "stored-child-1",
		}

		_, err = a.SendChildInput("child-1", agent.ChildInput{Content: "empty receipt"})
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.ActiveChildTurnState("child-1").Active)

		_, err = a.SendChildInput("child-1", agent.ChildInput{Content: "gateway failure"})
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.ActiveChildTurnState("child-1").Active)

		_, err = a.SendChildInput("child-1", agent.ChildInput{Content: "recovered"})
		require.NoError(t, err)
		assert.True(t, a.ActiveChildTurnState("child-1").Active)
	})

	t.Run("HTTP 400 is definite refusal and HTTP 502 after child turn start keeps active", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				http.Error(w, "bad child request", http.StatusBadRequest)
				return
			}
			close(entered)
			<-release
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}
		a.children["child-1"] = &nativeChild{
			descriptor: nativeChildDescriptor{ID: "child-1", Parent: "native-root", Mode: "continuable"},
			agentID:    "stored-child-1",
		}
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			Role: bgtask.RoleChildTask, RowKey: "child-1", Kind: bgtask.KindSubagent, ChildAgentID: "stored-child-1", Status: bgtask.StatusRunning,
		}))

		_, err = a.SendChildInput("child-1", agent.ChildInput{Content: "bad"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.ActiveChildTurnState("child-1").Active)

		done := make(chan error, 1)
		go func() {
			_, err := a.SendChildInput("child-1", agent.ChildInput{Content: "started before 502"})
			done <- err
		}()
		<-entered
		require.NoError(t, a.childTurnState("child-1", true, agent.MessageCompletionComplete))
		close(release)

		require.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
		assert.True(t, a.ActiveChildTurnState("child-1").Active)
	})
}

func TestResolveCatalogChildScopesByParentAndLeafName(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	childA := nativeChildDescriptor{ID: "child-a", Parent: "parent-1", Mode: "continuable", Name: "checker"}
	childB := nativeChildDescriptor{ID: "child-b", Parent: "parent-2", Mode: "continuable", Name: "checker"}
	a.Mu.Lock()
	a.registerCatalogChildLocked(childA)
	a.registerCatalogChildLocked(childB)

	id, desc, ok := a.resolveCatalogChildLocked("parent-1", "reviewer/checker")
	assert.True(t, ok)
	assert.Equal(t, "child-a", id)
	assert.Equal(t, childA, desc)

	id, desc, ok = a.resolveCatalogChildLocked("parent-2", "checker")
	assert.True(t, ok)
	assert.Equal(t, "child-b", id)
	assert.Equal(t, childB, desc)

	_, _, ok = a.resolveCatalogChildLocked("parent-3", "checker")
	assert.False(t, ok)

	_, _, ok = a.resolveCatalogChildLocked("parent-1", "reviewer/")
	assert.False(t, ok)

	_, _, ok = a.resolveCatalogChildLocked("parent-1", "")
	assert.False(t, ok)

	unnamed := nativeChildDescriptor{ID: "child-unnamed", Parent: "parent-1", Mode: "continuable"}
	a.registerCatalogChildLocked(unnamed)
	assert.NotContains(t, a.childCatalogByName, childNameKey{parentSessionID: "parent-1", name: ""})
	a.Mu.Unlock()
}

type failingChildRegistrySink struct {
	*agenttest.Sink
	reviveErr error
	updateErr error
	closeErr  error
}

func (s *failingChildRegistrySink) ReviveBackgroundTask(rowKey string) error {
	if s.reviveErr != nil {
		return s.reviveErr
	}
	return s.Sink.ReviveBackgroundTask(rowKey)
}

func (s *failingChildRegistrySink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.Sink.UpdateBackgroundTaskStatus(rowKey, status, activeForm)
}

func (s *failingChildRegistrySink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	return s.Sink.CloseBackgroundTask(rowKey, status)
}

func TestChildTurnStatePublishesTurnStateEvenWhenRegistryFails(t *testing.T) {
	t.Parallel()
	cause := errors.New("registry write failed")
	for _, tc := range []struct {
		name      string
		active    bool
		reviveErr error
		updateErr error
		closeErr  error
		wantState agent.TurnState
	}{
		{"revive fails", true, cause, nil, nil, agent.TurnState{Active: true, Steerable: true}},
		{"update fails", true, nil, cause, nil, agent.TurnState{Active: true, Steerable: true}},
		{"close fails", false, nil, nil, cause, agent.TurnState{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := &agenttest.Sink{}
			sink := &failingChildRegistrySink{
				Sink:      base,
				reviveErr: tc.reviveErr,
				updateErr: tc.updateErr,
				closeErr:  tc.closeErr,
			}
			a := newOfflineAgent(t, base)
			a.sink = agent.NewProviderServices(sink)
			a.children["child"] = &nativeChild{
				descriptor: nativeChildDescriptor{ID: "child", Parent: "native-root", Mode: "continuable"},
				agentID:    "stored-child",
				active:     !tc.active,
			}
			require.NoError(t, base.UpsertBackgroundTask(bgtask.Upsert{
				Role: bgtask.RoleChildTask, RowKey: "child", Kind: bgtask.KindSubagent, ChildAgentID: "stored-child", Status: bgtask.StatusRunning,
			}))

			err := a.childTurnState("child", tc.active, agent.MessageCompletionError)
			require.ErrorIs(t, err, cause)
			assert.Equal(t, tc.active, a.ActiveChildTurnState("child").Active)
			assert.Equal(t, []agent.TurnState{tc.wantState}, base.Child("stored-child").TurnStates(),
				"Worker turn state must stay synchronized with child.active when background-task registry fails")
		})
	}
}

func setupContinuableChildForSteerTest(t *testing.T, sink *agenttest.Sink, childKey string, active bool) (*Agent, *nativeChild, *sessionStream, *agenttest.Sink) {
	t.Helper()
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: childKey, Parent: "native-root", Mode: "continuable", Label: "Native child"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID,
		AgentSessionID:   descriptor.ID,
		Title:            descriptor.Label,
		Options:          optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode},
	})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:         bgtask.RoleChildTask,
		RowKey:       descriptor.ID,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: virtualID,
		Title:        descriptor.Label,
		Status:       bgtask.StatusRunning,
	}))
	child := &nativeChild{descriptor: descriptor, agentID: virtualID, active: active}
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = child
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
	return a, child, childStream, sink.Child(virtualID)
}

func applyChildSessionEvent(t *testing.T, a *Agent, seq int, kind string, data any) {
	t.Helper()
	rawData, err := json.Marshal(data)
	require.NoError(t, err)
	frame, err := json.Marshal(map[string]any{
		"type":     "item",
		"streamId": "child-stream",
		"value": map[string]any{
			"type": "event",
			"event": map[string]any{
				"type": kind,
				"seq":  seq,
				"time": 1000,
				"data": json.RawMessage(rawData),
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, a.handleFrame(frame))
}

func TestInterruptChildRemovesQueuedSteerAndReportsDroppedOnChildSink(t *testing.T) {
	t.Parallel()
	for _, spliceBeforeReply := range []bool{false, true} {
		t.Run(fmt.Sprintf("splice_before_reply=%t", spliceBeforeReply), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", true)

			var mu sync.Mutex
			var methods []string
			var gotSessionID, gotItemID, gotAction string
			var requestID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							ChildSessionID string `json:"childSessionId"`
							Request        struct {
								RequestID string `json:"requestId"`
								SessionID string `json:"sessionId"`
								ItemID    string `json:"itemId"`
								Action    struct {
									Kind string `json:"kind"`
								} `json:"action"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
					return
				}
				mu.Lock()
				methods = append(methods, r.URL.Path)
				mu.Unlock()
				value := map[string]any{"accepted": true}
				switch r.URL.Path {
				case "/api/subagents/prompt":
					mu.Lock()
					requestID = req.Payload.Args.Request.RequestID
					mu.Unlock()
					value = map[string]any{"messageId": "child-item-1"}
				case "/api/subagents/interruptByUser":
					assert.Equal(t, "native-child", req.Payload.Args.ChildSessionID)
				case "/api/session/updateQueue":
					input := req.Payload.Args.Request
					mu.Lock()
					gotSessionID = input.SessionID
					gotItemID = input.ItemID
					gotAction = input.Action.Kind
					mu.Unlock()
					if spliceBeforeReply {
						applyChildSessionEvent(t, a, 2, "agent/inbox/spliced", map[string]any{
							"target":       "next-step",
							"start":        0,
							"removedCount": 1,
							"inserted":     []any{},
							"outcome":      "canceled",
						})
					}
				default:
					t.Errorf("unexpected native request: %s", r.URL.Path)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": req.ID,
					"result": map[string]any{"ok": true, "value": value},
				}))
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a.rpc = remoteRPC{endpoint: endpoint}

			_, err = a.SteerChildInput("native-child", agent.SteerRequest{
				InputID: "child-input-1", AttemptID: "attempt-1", Content: "steer child",
			})
			require.NoError(t, err)
			mu.Lock()
			gotRequestID := requestID
			mu.Unlock()
			applyChildSessionEvent(t, a, 1, "agent/inbox/spliced", map[string]any{
				"target": "next-step",
				"start":  0,
				"inserted": []any{
					map[string]any{"id": "child-item-1", "source": map[string]string{"kind": "user", "rpcId": gotRequestID}},
				},
			})

			require.NoError(t, a.InterruptChild("native-child", agent.StopContext{}))
			if !spliceBeforeReply {
				// A trailing cancel splice after session/updateQueue returns must not report a duplicate drop.
				applyChildSessionEvent(t, a, 2, "agent/inbox/spliced", map[string]any{
					"target":       "next-step",
					"start":        0,
					"removedCount": 1,
					"inserted":     []any{},
					"outcome":      "canceled",
				})
			}

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []string{"/api/subagents/prompt", "/api/subagents/interruptByUser", "/api/session/updateQueue"}, methods)
			assert.Equal(t, "native-child", gotSessionID)
			assert.Equal(t, "child-item-1", gotItemID)
			assert.Equal(t, "remove", gotAction)
			assert.Equal(t, []agent.SteerResolution{{
				InputID: "child-input-1", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeDropped,
			}}, childSink.SteerResolutions(), "InterruptChild must report SteerOutcomeDropped once on the child sink")
			assert.Empty(t, sink.SteerResolutions(), "child steer drop must not report on the root sink")
			a.Mu.Lock()
			assert.Empty(t, child.pendingSteers)
			a.Mu.Unlock()
			a.dispatchMu.Lock()
			assert.Empty(t, a.replySubmissions)
			a.dispatchMu.Unlock()
		})
	}
}

func TestChildInboxSpliceCancellationDropsSteerAndIgnoresClaimAndReplace(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", true)

	var reqMu sync.Mutex
	var requestIDs []string
	var itemCounter atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Request struct {
						RequestID string `json:"requestId"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			return
		}
		reqMu.Lock()
		requestIDs = append(requestIDs, req.Payload.Args.Request.RequestID)
		reqMu.Unlock()
		itemID := fmt.Sprintf("child-item-%d", itemCounter.Add(1))
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"type": "server-response", "rpcId": req.ID,
			"result": map[string]any{"ok": true, "value": map[string]any{"messageId": itemID}},
		}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}

	_, err = a.SteerChildInput("native-child", agent.SteerRequest{InputID: "in-1", AttemptID: "att-1", Content: "first"})
	require.NoError(t, err)
	_, err = a.SteerChildInput("native-child", agent.SteerRequest{InputID: "in-2", AttemptID: "att-2", Content: "second"})
	require.NoError(t, err)
	reqMu.Lock()
	req1, req2 := requestIDs[0], requestIDs[1]
	reqMu.Unlock()

	applyChildSessionEvent(t, a, 1, "agent/inbox/spliced", map[string]any{
		"target": "next-step",
		"start":  0,
		"inserted": []any{
			map[string]any{"id": "child-item-1", "source": map[string]string{"kind": "user", "rpcId": req1}},
		},
	})
	applyChildSessionEvent(t, a, 2, "agent/inbox/spliced", map[string]any{
		"target": "next-step",
		"start":  1,
		"inserted": []any{
			map[string]any{"id": "child-item-2", "source": map[string]string{"kind": "user", "rpcId": req2}},
		},
	})

	// Replace keeps the same item pending and must not report SteerOutcomeDropped.
	applyChildSessionEvent(t, a, 3, "agent/inbox/spliced", map[string]any{
		"target":       "next-step",
		"start":        1,
		"removedCount": 1,
		"inserted": []any{
			map[string]any{"id": "child-item-2", "source": map[string]string{"kind": "user", "rpcId": req2}},
		},
		"outcome": "canceled",
	})
	assert.Empty(t, childSink.SteerResolutions())

	// Claim (no canceled outcome) removes index 0 from the inbox without dropping it.
	applyChildSessionEvent(t, a, 4, "agent/inbox/spliced", map[string]any{
		"target":       "next-step",
		"start":        0,
		"removedCount": 1,
		"inserted":     []any{},
	})
	assert.Empty(t, childSink.SteerResolutions())

	// Canceled remove of the remaining item (now at index 0) reports SteerOutcomeDropped on the child sink.
	applyChildSessionEvent(t, a, 5, "agent/inbox/spliced", map[string]any{
		"target":       "next-step",
		"start":        0,
		"removedCount": 1,
		"inserted":     []any{},
		"outcome":      "canceled",
	})
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "in-2", AttemptID: "att-2", Outcome: agent.SteerOutcomeDropped,
	}}, childSink.SteerResolutions())
	assert.Empty(t, sink.SteerResolutions())
	a.Mu.Lock()
	require.Len(t, child.pendingSteers, 1)
	assert.Equal(t, req1, child.pendingSteers[0].requestID)
	a.Mu.Unlock()
}

func TestInterruptChildHandlesColdAgentBusyAndNotFoundFromUpdateQueue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		code            string
		active          bool
		wantErr         bool
		wantRemaining   int
		wantUpdateCalls int32
	}{
		{name: "cold_child_agent_busy_active", code: "session/agent-busy", active: true, wantErr: false, wantRemaining: 1, wantUpdateCalls: 1},
		{name: "cold_child_agent_busy_idle", code: "session/agent-busy", active: false, wantErr: false, wantRemaining: 1, wantUpdateCalls: 1},
		{name: "queue_item_not_found_active", code: "session/queue-item-not-found", active: true, wantErr: false, wantRemaining: 0, wantUpdateCalls: 1},
		{name: "other_error_fails_interrupt", code: "session/unavailable", active: true, wantErr: true, wantRemaining: 1, wantUpdateCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", tc.active)
			child.pendingSteers = []*pendingSteer{{
				requestID: "req-1",
				itemID:    "child-item-1",
				request:   agent.SteerRequest{InputID: "in-1", AttemptID: "att-1", Content: "steer"},
			}}
			var updateCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID string `json:"rpcId"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
					return
				}
				result := map[string]any{"ok": true, "value": map[string]any{"accepted": true}}
				if r.URL.Path == "/api/session/updateQueue" {
					updateCalls.Add(1)
					result = map[string]any{"ok": false, "error": map[string]any{"code": tc.code, "message": "native refusal"}}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": req.ID, "result": result,
				}))
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a.rpc = remoteRPC{endpoint: endpoint}

			err = a.InterruptChild("native-child", agent.StopContext{})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantUpdateCalls, updateCalls.Load())
			assert.Empty(t, childSink.SteerResolutions(), "unconfirmed removal must not report SteerOutcomeDropped")
			a.Mu.Lock()
			assert.Len(t, child.pendingSteers, tc.wantRemaining)
			a.Mu.Unlock()
		})
	}
}

func TestDeliveredChildSteerSkipsUpdateQueueOnInterruptChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", true)

	var mu sync.Mutex
	var methods []string
	var requestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Request struct {
						RequestID string `json:"requestId"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			return
		}
		mu.Lock()
		methods = append(methods, r.URL.Path)
		mu.Unlock()
		value := map[string]any{"accepted": true}
		if r.URL.Path == "/api/subagents/prompt" {
			mu.Lock()
			requestID = req.Payload.Args.Request.RequestID
			mu.Unlock()
			value = map[string]any{"messageId": "child-item-1"}
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"type": "server-response", "rpcId": req.ID,
			"result": map[string]any{"ok": true, "value": value},
		}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}

	applyChildSessionEvent(t, a, 1, "turn/start", map[string]any{"turn": 4})
	_, err = a.SteerChildInput("native-child", agent.SteerRequest{
		InputID: "child-in-1", AttemptID: "child-att-1", Content: "deliver me",
	})
	require.NoError(t, err)
	mu.Lock()
	gotRequestID := requestID
	mu.Unlock()
	applyChildSessionEvent(t, a, 2, "agent/inbox/spliced", map[string]any{
		"target": "next-step",
		"start":  0,
		"inserted": []any{
			map[string]any{"id": "child-item-1", "source": map[string]string{"kind": "user", "rpcId": gotRequestID}},
		},
	})
	applyChildSessionEvent(t, a, 3, "user/message", map[string]any{
		"source": map[string]string{"kind": "user", "rpcId": gotRequestID},
	})

	require.NoError(t, a.InterruptChild("native-child", agent.StopContext{}))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/api/subagents/prompt", "/api/subagents/interruptByUser"}, methods,
		"delivered child steer must not call session/updateQueue during InterruptChild")
	assert.Equal(t, []agent.SteerResolution{{
		InputID:   "child-in-1",
		AttemptID: "child-att-1",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt:   agent.NativeInputReceipt{SessionID: "native-child", InputID: gotRequestID, TurnID: "4"},
	}}, childSink.SteerResolutions())
	a.Mu.Lock()
	assert.Empty(t, child.pendingSteers)
	a.Mu.Unlock()
}

func TestStopClearsChildPendingSteersAlongsideUnfinishedReplies(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", true)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "deepseek-child-stop", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"type": "server-response", "rpcId": req.ID,
			"result": map[string]any{"ok": true, "value": map[string]any{"messageId": "child-item-stop"}},
		}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}

	_, err = a.SteerChildInput("native-child", agent.SteerRequest{
		InputID: "child-in-stop", AttemptID: "child-att-stop", Content: "pending at stop",
	})
	require.NoError(t, err)
	a.Mu.Lock()
	require.Len(t, child.pendingSteers, 1)
	a.Mu.Unlock()

	a.Mu.Lock()
	a.children["one-shot-active"] = &nativeChild{
		descriptor: nativeChildDescriptor{ID: "one-shot-active", Parent: "native-root", Mode: "one-shot"},
		agentID:    "stored-one-shot",
		active:     true,
	}
	a.Mu.Unlock()

	a.Stop()

	a.Mu.Lock()
	assert.Empty(t, child.pendingSteers, "Stop must clear pending child steers")
	assert.False(t, a.children["one-shot-active"].active)
	a.Mu.Unlock()
	assert.False(t, a.ActiveChildTurnState("native-child").Active)
	assert.Equal(t, []agent.TurnState{{}}, childSink.TurnStates(), "Stop must publish idle turn state for active continuable children")
	assert.Empty(t, sink.Child("stored-one-shot").TurnStates(), "Stop must not publish turn state for one-shot children")
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "child-in-stop", AttemptID: "child-att-stop", Outcome: agent.SteerOutcomeUncertain,
	}}, childSink.SteerResolutions())
}

func TestSteerChildInputErrorCleanupHonorsDeliveredBeforeError(t *testing.T) {
	t.Parallel()
	for _, deliveredBeforeError := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%t", deliveredBeforeError), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, child, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", true)
			applyChildSessionEvent(t, a, 1, "turn/start", map[string]any{"turn": 9})

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							Request struct {
								RequestID string `json:"requestId"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
					return
				}
				if deliveredBeforeError {
					applyChildSessionEvent(t, a, 2, "user/message", map[string]any{
						"source": map[string]string{"kind": "user", "rpcId": req.Payload.Args.Request.RequestID},
					})
				}
				w.WriteHeader(http.StatusBadGateway)
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a.rpc = remoteRPC{endpoint: endpoint}

			_, err = a.SteerChildInput("native-child", agent.SteerRequest{
				InputID: "child-in-err", AttemptID: "child-att-err", Content: "steer",
			})
			if deliveredBeforeError {
				require.NoError(t, err)
				require.Len(t, childSink.SteerResolutions(), 1)
				assert.Equal(t, agent.SteerOutcomeEchoed, childSink.SteerResolutions()[0].Outcome)
			} else {
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
				assert.Empty(t, childSink.SteerResolutions())
			}
			a.Mu.Lock()
			assert.Empty(t, child.pendingSteers)
			a.Mu.Unlock()
			a.dispatchMu.Lock()
			assert.Empty(t, a.replySubmissions)
			a.dispatchMu.Unlock()
		})
	}
}

func TestSendChildInputEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("stopped and streamFailure refuse child input before RPC", func(t *testing.T) {
		t.Parallel()
		for _, reason := range []string{"stopped", "streamFailure"} {
			t.Run(reason, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a, _, _, _ := setupContinuableChildForSteerTest(t, sink, "native-child", false)
				if reason == "stopped" {
					a.SetStoppedForTest(true)
				} else {
					a.Mu.Lock()
					a.streamFailure = errors.New("broken stream")
					a.Mu.Unlock()
				}

				_, err := a.SendChildInput("native-child", agent.ChildInput{Content: "hello"})
				require.Error(t, err)
				_, err = a.SteerChildInput("native-child", agent.SteerRequest{InputID: "in-1", Content: "steer"})
				require.Error(t, err)
			})
		}
	})

	t.Run("SteerChildInput on idle child returns ErrNoActiveTurn and non-image attachment is rejected", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a, _, _, _ := setupContinuableChildForSteerTest(t, sink, "native-child", false)

		_, err := a.SteerChildInput("native-child", agent.SteerRequest{InputID: "in-1", Content: "steer"})
		require.ErrorIs(t, err, agent.ErrNoActiveTurn)

		_, err = a.SendChildInput("native-child", agent.ChildInput{
			Content:     "with file",
			Attachments: []*leapmuxv1.Attachment{{Filename: "doc.txt", MimeType: "text/plain", Data: []byte("abc")}},
		})
		require.ErrorContains(t, err, "accepts text and images")
	})

	t.Run("HTTP 408 is uncertain while remoteFailure is a definite refusal", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			if calls.Add(1) == 1 {
				http.Error(w, "timeout", http.StatusRequestTimeout)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": false, "error": map[string]any{"code": "subagent/refused", "message": "refused"}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a, _, _, _ := setupContinuableChildForSteerTest(t, sink, "native-child", false)
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SendChildInput("native-child", agent.ChildInput{Content: "timeout"})
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.ActiveChildTurnState("native-child").Active)

		_, err = a.SendChildInput("native-child", agent.ChildInput{Content: "refused"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.ActiveChildTurnState("native-child").Active)
	})

	t.Run("untracked child steer honors pending.delivered on HTTP 502 error", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a, child, _, _ := setupContinuableChildForSteerTest(t, sink, "native-child", true)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Payload struct {
					Args struct {
						Request struct {
							RequestID string `json:"requestId"`
						} `json:"request"`
					} `json:"args"`
				} `json:"payload"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			applyChildSessionEvent(t, a, 1, "user/message", map[string]any{
				"source": map[string]string{"kind": "user", "rpcId": req.Payload.Args.Request.RequestID},
			})
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SteerChildInput("native-child", agent.SteerRequest{InputID: "in-untracked", Content: "steer"})
		require.NoError(t, err)
		a.Mu.Lock()
		assert.Empty(t, child.pendingSteers)
		a.Mu.Unlock()
	})
}

func TestChildTurnStateAndInterruptChildTolerateNilSinkAndEmptyStreams(t *testing.T) {
	t.Parallel()

	t.Run("childTurnState with nil sink updates active without panicking", func(t *testing.T) {
		t.Parallel()
		a := newOfflineAgentServing(t, nil, nil)
		a.children["child"] = &nativeChild{
			descriptor: nativeChildDescriptor{ID: "child", Parent: "native-root", Mode: "continuable"},
			agentID:    "stored-child",
		}
		require.NoError(t, a.childTurnState("child", true, ""))
		assert.True(t, a.ActiveChildTurnState("child").Active)
		require.NoError(t, a.childTurnState("child", false, agent.MessageCompletionComplete))
		assert.False(t, a.ActiveChildTurnState("child").Active)
	})

	t.Run("InterruptChild on active child stops on non-404 error without fallback or updateQueue", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a, child, _, _ := setupContinuableChildForSteerTest(t, sink, "native-child", true)
		child.pendingSteers = []*pendingSteer{{requestID: "req-1", itemID: "item-1", request: agent.SteerRequest{InputID: "in-1", AttemptID: "att-1"}}}
		a.rpc = remoteRPC{endpoint: endpoint}

		require.Error(t, a.InterruptChild("native-child", agent.StopContext{}))
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"/api/subagents/interruptByUser"}, paths)
	})

	t.Run("InterruptChild on idle child without tracked stream resets spans and progress", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a, _, _, childSink := setupContinuableChildForSteerTest(t, sink, "native-child", false)
		delete(a.streams, "child-stream")

		require.NoError(t, a.InterruptChild("native-child", agent.StopContext{}))
		assert.Contains(t, childSink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, childSink.ProgressUpdates(), agent.ResetProgress())
	})

	t.Run("InterruptChild on idle child with nil sink returns nil without panicking", func(t *testing.T) {
		t.Parallel()
		a := newOfflineAgentServing(t, nil, nil)
		a.children["native-child"] = &nativeChild{
			descriptor: nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable"},
			agentID:    "stored-child",
		}
		require.NoError(t, a.InterruptChild("native-child", agent.StopContext{}))
	})
}
