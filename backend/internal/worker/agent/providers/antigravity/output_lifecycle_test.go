package antigravity

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type turnEndTestSink struct {
	agent.ProviderServices
	persist func(agent.MessageContent, agent.SpanInfo) error
}

func (s *turnEndTestSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	return s.persist(content, span)
}

func TestAntigravityTurnFinalizationHasOneOwner(t *testing.T) {
	a, sink := testAgent(t)
	base := a.sink
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	var writes atomic.Int32
	writer := &turnEndTestSink{ProviderServices: base, persist: func(content agent.MessageContent, span agent.SpanInfo) error {
		if writes.Add(1) == 1 {
			close(entered)
			<-release
		}
		return base.PersistTurnEnd(content, span)
	}}
	a.sink, a.root.sink = writer, writer
	a.beginTurn(a.root)
	done := make(chan struct{})
	go func() {
		assert.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
		close(done)
	}()
	receiveTestValue(t, entered)
	assert.NoError(t, a.endTurn(a.root, agent.MessageCompletionInterrupted))
	assert.Equal(t, int32(1), writes.Load())
	unblock()
	receiveTestValue(t, done)
	var dividers int
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			dividers++
		}
	}
	assert.Equal(t, 1, dividers)
	assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)
}

func TestAntigravityFinalizationRefusesInputAndSteering(t *testing.T) {
	a, _ := testAgent(t)
	var delivered atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == languageServerService+"SendUserCascadeMessage" {
			delivered.Add(1)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	base := a.sink
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	writer := &turnEndTestSink{ProviderServices: base, persist: func(content agent.MessageContent, span agent.SpanInfo) error {
		close(entered)
		<-release
		return base.PersistTurnEnd(content, span)
	}}
	a.sink, a.root.sink = writer, writer
	a.beginTurn(a.root)
	done := make(chan struct{})
	go func() {
		assert.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
		close(done)
	}()
	receiveTestValue(t, entered)
	assert.ErrorIs(t, a.SendInput("next turn", nil), agent.ErrAgentBusy)
	assert.ErrorIs(t, a.SteerInput("too late to steer", nil), agent.ErrAgentBusy)
	assert.Zero(t, delivered.Load())
	unblock()
	receiveTestValue(t, done)
	a.beginTurn(a.root)
	assert.True(t, a.PublishTurnActive().Active)
}

func TestAntigravityDiscardOutputSuppressesStreamWrites(t *testing.T) {
	for _, childOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "child"}[childOutput], func(t *testing.T) {
			a, sink := testAgent(t)
			state := a.root
			if childOutput {
				state = newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
				state.parent = a.root
				a.cascades[state.id] = state
			}
			a.DiscardOutput()
			require.NoError(t, a.applyUpdate(state, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{
				plannerStep(contracts.AntigravityStepStatusDone, "discard this answer"),
			}), false))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.Child("child-agent").Messages())
			assert.Empty(t, sink.TurnActiveCalls)
		})
	}
}

func TestAntigravityDiscardOutputSuppressesBufferedTeardownWrites(t *testing.T) {
	a, sink := testAgent(t)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{
		plannerStep("CORTEX_STEP_STATUS_RUNNING", "discard this partial answer"),
	}))
	errorStep := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_ERROR_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","errorMessage":{"error":{"shortError":"discard this failure"}}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{1}, []json.RawMessage{errorStep}))
	require.Empty(t, sink.Messages())
	a.DiscardOutput()
	a.finishCascades([]*cascadeState{a.root}, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	require.NotEmpty(t, sink.TurnActiveCalls)
	assert.False(t, sink.TurnActiveCalls[len(sink.TurnActiveCalls)-1])
}

func TestAntigravityRetiredTurnCannotResetReplacementProgress(t *testing.T) {
	a, sink := testAgent(t)
	base := a.sink
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	writer := &turnEndTestSink{ProviderServices: base, persist: func(content agent.MessageContent, span agent.SpanInfo) error {
		close(entered)
		<-release
		return base.PersistTurnEnd(content, span)
	}}
	a.sink, a.root.sink = writer, writer
	retired := a.root
	a.beginTurn(retired)
	done := make(chan error, 1)
	go func() { done <- a.endTurn(retired, agent.MessageCompletionComplete) }()
	receiveTestValue(t, entered)
	a.Mu.Lock()
	a.generation++
	replacement := a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.beginTurn(replacement)
	replacement.sink.OpenSpan("replacement-span", "")
	replacement.sink.ReportProgress(agent.ModelTextProgress("replacement-progress", "keep this progress"))
	spans := sink.OpenSpans()
	progress := sink.ProgressUpdates()
	resets := sink.ResetSpanCount()
	states := sink.TurnStates()
	unblock()
	require.NoError(t, receiveTestValue(t, done))
	assert.Equal(t, states, sink.TurnStates())
	assert.Equal(t, spans, sink.OpenSpans())
	assert.Equal(t, resets, sink.ResetSpanCount())
	assert.Equal(t, progress, sink.ProgressUpdates())
	assert.Equal(t, cascadeTurnActive, replacement.turnPhase)
}
func TestAntigravityRetiredInstalledTurnStillFinishes(t *testing.T) {
	a, sink := testAgent(t)
	a.beginTurn(a.root)
	a.Mu.Lock()
	a.generation++
	a.Mu.Unlock()
	a.finishCascades([]*cascadeState{a.root}, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	assert.Equal(t, cascadeTurnIdle, a.root.turnPhase)
	assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)
	dividers := 0
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			dividers++
		}
	}
	assert.Equal(t, 1, dividers)
}
