package deepseekharness

import (
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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
		assert.ErrorIs(t, a.SendChildInput(key, "message", nil), agent.ErrChildOperationUnsupported)
		assert.ErrorIs(t, a.SteerChildInput(key, "message", nil), agent.ErrChildOperationUnsupported)
		assert.ErrorIs(t, a.InterruptChild(key, agent.StopContext{}), agent.ErrChildOperationUnsupported)
	}
}

func TestContinuableResultAttachesOnlyItsExactFirstSpawnSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Native child"}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
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
