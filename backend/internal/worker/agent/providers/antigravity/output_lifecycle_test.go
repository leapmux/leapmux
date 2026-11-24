package antigravity

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	assert.ErrorIs(t, a.SteerInput(agent.SteerRequest{Content: "too late to steer"}), agent.ErrAgentBusy)
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

func TestAntigravityFinishCascadesClosesIdleOpenToolSteps(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-idle"}},"runCommand":{"command":"sleep 30"}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.Equal(t, cascadeTurnIdle, a.root.turnPhase)
	require.Empty(t, sink.ClosedSpans())

	a.finishCascades([]*cascadeState{a.root}, agent.MessageCompletionError, bgtask.StatusFailed)
	assert.Contains(t, sink.ClosedSpans(), "cmd-idle")
	assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

func TestAntigravityFatalCascadeClosesOpenToolStepsAndResetsSpans(t *testing.T) {
	t.Parallel()

	t.Run("active child cascade closes open tool step and resets spans and progress", func(t *testing.T) {
		t.Parallel()
		a, sink, child := childLifecycleTestAgent(t)
		rpc, _ := fakeAntigravityRPC(t)
		a.rpc = rpc
		childSink := sink.Child(child.childAgentID)
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-child-active"}},"runCommand":{"command":"sleep 30"}}`)
		require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}), false))
		require.Equal(t, cascadeTurnActive, child.turnPhase)
		require.Empty(t, childSink.ClosedSpans())

		a.fatalCascade(child, errors.New("child stream lost"))

		assert.Equal(t, cascadeTurnIdle, child.turnPhase)
		assert.Contains(t, childSink.ClosedSpans(), "cmd-child-active")
		assert.Contains(t, childSink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, childSink.ProgressUpdates(), agent.ResetProgress())
		row, ok := sink.BackgroundTask(child.id)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
	})

	t.Run("idle child cascade with open cross-turn tool step closes step and resets spans and progress", func(t *testing.T) {
		t.Parallel()
		a, sink, child := childLifecycleTestAgent(t)
		rpc, _ := fakeAntigravityRPC(t)
		a.rpc = rpc
		childSink := sink.Child(child.childAgentID)
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-child-idle"}},"runCommand":{"command":"sleep 30"}}`)
		require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}), false))
		require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
		require.Equal(t, cascadeTurnIdle, child.turnPhase)
		require.False(t, child.rowOpen)
		require.Empty(t, childSink.ClosedSpans())

		a.fatalCascade(child, errors.New("idle child stream lost"))

		assert.Contains(t, childSink.ClosedSpans(), "cmd-child-idle")
		assert.Contains(t, childSink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, childSink.ProgressUpdates(), agent.ResetProgress())
		assert.NotEmpty(t, childSink.LeapMuxNotifications())
	})

	t.Run("active root cascade closes open tool step before turn-end divider", func(t *testing.T) {
		t.Parallel()
		a, sink := testAgent(t)
		exited := make(chan struct{})
		close(exited)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: exited})
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-root-active"}},"runCommand":{"command":"sleep 30"}}`)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}))
		require.Equal(t, cascadeTurnActive, a.root.turnPhase)
		require.Empty(t, sink.ClosedSpans())

		a.fatalCascade(a.root, errors.New("root stream lost"))
		require.Eventually(t, a.IsStopped, 5*time.Second, 5*time.Millisecond)

		assert.Contains(t, sink.ClosedSpans(), "cmd-root-active")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())

		closingIndex, dividerIndex := -1, -1
		for i, msg := range sink.Messages() {
			if msg.SpanID == "cmd-root-active" && msg.Closing {
				closingIndex = i
			}
			if msg.TurnEnd {
				dividerIndex = i
			}
		}
		require.NotEqual(t, -1, closingIndex, "fatal root cascade must persist tool closing row")
		require.NotEqual(t, -1, dividerIndex, "fatal root cascade must persist turn-end divider")
		assert.Less(t, closingIndex, dividerIndex, "tool closing row must precede the turn-end divider")
	})

	t.Run("fatal root cascade with open background task and child closes steps before error turn end without intentional stop", func(t *testing.T) {
		t.Parallel()
		a, sink, child := childLifecycleTestAgent(t)
		exited := make(chan struct{})
		close(exited)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: exited})
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-root-bg"}},"runCommand":{"command":"sleep 30"}}`)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}))
		a.Mu.Lock()
		a.root.background["bg-shell"] = backgroundTask{rowKey: "bg-shell", title: "sleep 30", stepIndex: 0}
		a.Mu.Unlock()
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "bg-shell", Kind: bgtask.KindShell, Title: "sleep 30", Status: bgtask.StatusRunning}))
		require.Equal(t, cascadeTurnActive, a.root.turnPhase)

		a.fatalCascade(a.root, errors.New("root stream lost"))
		require.Eventually(t, a.IsStopped, 5*time.Second, 5*time.Millisecond)

		assert.Equal(t, agent.MessageCompletionError, a.ProcessExitCompletion(), "fatal root stream loss must classify exit as error, not interrupted")
		bgRow, ok := sink.BackgroundTask("bg-shell")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, bgRow.Status, "fatal root stream loss must fail the open background task")
		childRow, ok := sink.BackgroundTask(child.id)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, childRow.Status, "fatal root stream loss must fail open child rows")

		closingIndex, dividerIndex := -1, -1
		var dividerCompletion agent.MessageCompletion
		for i, msg := range sink.Messages() {
			if msg.SpanID == "cmd-root-bg" && msg.Closing {
				closingIndex = i
			}
			if msg.TurnEnd {
				dividerIndex = i
				dividerCompletion = msg.Completion
			}
		}
		require.NotEqual(t, -1, closingIndex, "fatal root cascade must persist background tool closing row")
		require.NotEqual(t, -1, dividerIndex, "fatal root cascade must persist turn-end divider")
		assert.Less(t, closingIndex, dividerIndex, "background tool closing row must precede the turn-end divider")
		assert.Equal(t, agent.MessageCompletionError, dividerCompletion)
	})
}
