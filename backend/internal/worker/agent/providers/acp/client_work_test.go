package acp

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientWorkOwnsOnlyItsManualBusyHold(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	work := b.BeginClientWork()
	require.NotNil(t, work)
	assert.True(t, b.PromptActive())
	assert.False(t, b.ConversationActive())
	assert.False(t, b.AgentTurnActive())
	assert.Nil(t, b.rootTarget)
	assert.Nil(t, b.BeginClientWork())
	assert.False(t, b.AdmitAgentTurn())
	assert.ErrorIs(t, b.SendInput("later", nil), agent.ErrAgentBusy)
	require.True(t, work.End(TurnVerdict{Failure: providerkit.CompactionEnd()}))
	assert.False(t, b.PromptActive())
	assert.Empty(t, sink.Messages())
	assert.Equal(t, []agent.TurnState{{Active: true}, {}}, sink.TurnStates())
	assert.Equal(t, 1, sink.AutoCancelCount())
	assert.False(t, work.End(transientVerdict()))
	assert.Zero(t, sink.AutoScheduleCount())
	next := b.BeginClientWork()
	require.NotNil(t, next)
	assert.False(t, work.End(transientVerdict()))
	assert.True(t, b.PromptActive(), "an old capability cannot release the replacement hold")
	assert.True(t, next.End(TurnVerdict{}))
}

func TestClientWorkConcurrentAdmissionHasOneOwner(t *testing.T) {
	t.Parallel()
	b, _ := newACPTurnBase(t, &agenttest.Stdin{})
	owners := make(chan *ClientWork, 32)
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() { owners <- b.BeginClientWork() })
	}
	done := make(chan struct{})
	go func() { defer close(done); group.Wait() }()
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("concurrent manual admissions did not finish")
	}
	close(owners)
	var admitted []*ClientWork
	for owner := range owners {
		if owner != nil {
			admitted = append(admitted, owner)
		}
	}
	require.Len(t, admitted, 1)
	assert.True(t, admitted[0].End(TurnVerdict{}))
}

func TestClientWorkKeepsNativeTurnsIndependent(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	work := b.BeginClientWork()
	require.NotNil(t, work)
	for range 2 {
		require.True(t, b.BeginAgentTurn())
		b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "native"))
		require.True(t, b.EndAgentTurnWithVerdict(json.RawMessage(agentTurnEndFrame), transientVerdict()))
		assert.True(t, b.PromptActive(), "the manual hold remains after native completion")
		assert.False(t, b.ConversationActive())
	}
	assert.Len(t, turnEndContents(sink), 2)
	assert.Equal(t, 2, sink.AutoScheduleCount())
	assert.True(t, work.End(TurnVerdict{Failure: providerkit.CompactionEnd()}))
	assert.Zero(t, sink.AutoCancelCount(), "manual completion cannot cancel a newer native verdict")
	assert.False(t, b.PromptActive())
}

func TestClientWorkEndRetainsItsQueuePositionAndPayload(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	work := b.BeginClientWork()
	require.NotNil(t, work)
	sink.PersistErr = assert.AnError
	writer := agent.NewOrderedProviderServices(agent.OutputQueueFor(b.sink), b.sink)
	require.True(t, agent.OutputRetained(writer.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"first":true}`)}, agent.SpanInfo{})))
	payload := []byte(`{"owner":"manual"}`)
	require.True(t, work.End(TurnVerdict{Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}, Payload: payload}))
	// The manager republishes state after a busy refusal. That report must
	// retain the verdict that still waits behind an earlier output write.
	assert.Equal(t, agent.TurnState{}, b.PublishTurnActive())
	payload[0] = '!'
	assert.Zero(t, sink.AutoScheduleCount())
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(b.sink))
	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.JSONEq(t, `{"owner":"manual"}`, string(sink.LastAutoSchedule().SourcePayload))
	operations := sink.Operations()
	var relevant []string
	for _, operation := range operations {
		if operation.Op == "turn_end_stored" || operation.Op == "schedule_server_error" || (operation.Op == "turn_active" && !operation.Active) {
			relevant = append(relevant, operation.Op)
		}
	}
	assert.Equal(t, []string{"turn_end_stored", "schedule_server_error", "turn_active", "turn_active"}, relevant)
}

type clientWorkCaptureBarrier struct {
	agent.ProviderServices
	entered chan struct{}
	release <-chan struct{}
}

func (sink *clientWorkCaptureBarrier) CaptureAutoRetryOperation(sequence uint64, apply func(agent.AutoRetryWriter)) func() error {
	close(sink.entered)
	<-sink.release
	return sink.ProviderServices.CaptureAutoRetryOperation(sequence, apply)
}

func TestClientWorkSlowCaptureUsesTheFinalStateSequence(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	barrier := &clientWorkCaptureBarrier{ProviderServices: b.sink, entered: make(chan struct{}), release: release}
	b.sink = barrier
	work := b.BeginClientWork()
	require.NotNil(t, work)
	done := make(chan struct{})
	go func() { defer close(done); work.End(TurnVerdict{}) }()
	select {
	case <-barrier.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("manual completion did not capture its authority")
	}
	require.True(t, b.BeginAgentTurn())
	require.True(t, b.EndAgentTurnWithVerdict(json.RawMessage(agentTurnEndFrame), transientVerdict()))
	previous := sink.TurnSeqs()
	require.NotEmpty(t, previous)
	nativeEndSequence := previous[len(previous)-1]
	unblock()
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("manual completion did not release its hold")
	}
	sequences := sink.TurnSeqs()
	assert.Greater(t, sequences[len(sequences)-1], nativeEndSequence)
	active, present := sink.LastTurnActive()
	require.True(t, present)
	assert.False(t, active)
	assert.Equal(t, 1, sink.AutoScheduleCount())
	assert.Zero(t, sink.AutoCancelCount())
}

func TestClientWorkReplacedOrAbsentOwnerChangesNothing(t *testing.T) {
	t.Parallel()
	var absent *ClientWork
	assert.False(t, absent.End(TurnVerdict{}))
	assert.Nil(t, (&Base{}).BeginClientWork())
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	work := b.BeginClientWork()
	require.NotNil(t, work)
	b.clearActivePrompt()
	replacement := b.BeginClientWork()
	require.NotNil(t, replacement)
	before := len(sink.TurnStates())
	assert.False(t, work.End(transientVerdict()))
	assert.Len(t, sink.TurnStates(), before)
	assert.True(t, b.PromptActive())
	assert.Zero(t, sink.AutoScheduleCount())
	assert.True(t, replacement.End(TurnVerdict{}))
}

func TestClientWorkSavedPublicationCannotRetireANewerNativeTurn(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	work := b.BeginClientWork()
	require.NotNil(t, work)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	publish := b.publishTurnActive
	b.publishTurnActive = func(state agent.TurnState, sequence uint64) {
		if !state.Active {
			close(entered)
			<-release
		}
		publish(state, sequence)
	}
	done := make(chan struct{})
	go func() { defer close(done); work.End(TurnVerdict{}) }()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("manual completion did not reach publication")
	}
	require.True(t, b.BeginAgentTurn())
	nativeWriter := sink.CaptureAutoRetry()
	unblock()
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("manual completion did not finish publication")
	}
	b.publishTurnActive = publish
	assert.True(t, b.AgentTurnActive())
	nativeWriter.ScheduleServerErrorRetry([]byte(`{"native":true}`))
	require.Equal(t, 1, sink.AutoScheduleCount(), "the saved old idle must not retire the new native authority")
	assert.Equal(t, 1, sink.AutoCancelCount())
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
}
