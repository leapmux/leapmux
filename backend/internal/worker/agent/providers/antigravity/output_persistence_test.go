package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type transcriptFailureSink struct {
	agent.ProviderServices
	failAt atomic.Int32
	calls  atomic.Int32
	stored chan agent.MessageContent
}

func (s *transcriptFailureSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.calls.Add(1) == s.failAt.Load() {
		return errors.New("transcript storage is unavailable")
	}
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	s.stored <- content.Clone()
	return nil
}

func TestAntigravityPersistenceRecoversWithoutAnotherNativeFrame(t *testing.T) {
	for _, failureAt := range []int32{1, 2} {
		t.Run(fmt.Sprintf("failure at write %d", failureAt), func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			rawSink := &agenttest.Sink{}
			queue := rawSink.OutputQueue()
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
			sink := &transcriptFailureSink{ProviderServices: agent.NewProviderServices(rawSink), stored: make(chan agent.MessageContent, 8)}
			sink.failAt.Store(failureAt)
			a := newAgentState(sink, antigravityRPC{}, t.TempDir(), clock)
			t.Cleanup(a.streamCancel)
			a.installRoot("cascade", false)
			raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"modifiedResponse":"visible answer","thinking":"retained reasoning"}}`)
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
			require.True(t, queue.HasPending(), "the complete batch must survive a failed write")
			sink.failAt.Store(0)
			ctx := testutil.DeadlineContext(t)
			delay := testutil.WaitForTimer(t, ctx, newTimer)
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
			first := receiveTestValue(t, sink.stored)
			second := receiveTestValue(t, sink.stored)
			queue.StopRetries()
			assert.False(t, queue.HasPending())
			var thinking, answer antigravityStep
			require.NoError(t, json.Unmarshal(first.Original, &thinking))
			require.NoError(t, json.Unmarshal(second.Original, &answer))
			assert.Equal(t, "retained reasoning", thinking.PlannerResponse.Thinking)
			assert.Empty(t, thinking.PlannerResponse.ModifiedResponse)
			assert.Equal(t, "visible answer", answer.PlannerResponse.ModifiedResponse)
			assert.NotEqual(t, first.IdempotencyKey, second.IdempotencyKey)
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
			var stored int
			for _, message := range rawSink.Messages() {
				if message.Stored() {
					stored++
				}
			}
			assert.Equal(t, 2, stored, "replay must not submit a second copy of a retained batch")
		})
	}
}

type transcriptAdmissionSink struct {
	agent.ProviderServices
	refuse atomic.Bool
}

func (s *transcriptAdmissionSink) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	if s.refuse.Load() {
		return nil, errors.New("transcript admission is unavailable")
	}
	return s.ProviderServices.ReserveTranscriptOutput(queue, writes...)
}

func (s *transcriptAdmissionSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.refuse.Load() {
		return errors.New("transcript admission is unavailable")
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func TestAntigravityPersistenceFailurePinsReplayCursor(t *testing.T) {
	for _, raw := range []json.RawMessage{
		plannerStep(contracts.AntigravityStepStatusDone, "recover this answer"),
		json.RawMessage(`{"type":"CORTEX_STEP_TYPE_FUTURE","status":"CORTEX_STEP_STATUS_DONE","future":{"value":9007199254740993}}`),
		json.RawMessage(`{"future":{"value":9007199254740993}}`),
	} {
		t.Run(string(raw), func(t *testing.T) {
			rawSink := &agenttest.Sink{}
			sink := &transcriptAdmissionSink{ProviderServices: agent.NewProviderServices(rawSink)}
			sink.refuse.Store(true)
			a := newAgentState(sink, antigravityRPC{}, t.TempDir(), nil)
			t.Cleanup(a.streamCancel)
			a.installRoot("cascade", false)
			update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw})
			err := a.applyUpdate(a.root, update, false)
			assert.Error(t, err, "the stream must learn that output was not admitted")
			assert.False(t, rawSink.OutputQueue().HasPending())
			a.Mu.Lock()
			assert.Equal(t, 0, a.firstOpenIndexLocked(a.root))
			a.Mu.Unlock()
			sink.refuse.Store(false)
			require.NoError(t, a.applyUpdate(a.root, update, false))
			a.Mu.Lock()
			assert.Equal(t, 1, a.firstOpenIndexLocked(a.root))
			a.Mu.Unlock()
			messages := rawSink.Messages()
			require.Len(t, messages, 1)
			assert.True(t, messages[0].Stored())
			assert.JSONEq(t, string(raw), string(messages[0].Content))
		})
	}
}

func TestAntigravityPersistenceLiveFailurePinsReplayCursor(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			rawSink := &agenttest.Sink{}
			sink := &transcriptAdmissionSink{ProviderServices: agent.NewProviderServices(rawSink)}
			sink.refuse.Store(true)
			a := newAgentState(sink, antigravityRPC{}, t.TempDir(), nil)
			t.Cleanup(a.streamCancel)
			a.installRoot("cascade", false)
			if background {
				a.root.background[0] = backgroundTask{rowKey: "shell", stepIndex: 0}
			}
			raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"live-command","name":"run_command"}},"runCommand":{"commandLine":"echo marker"}}`)
			update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw})
			assert.Error(t, a.applyUpdate(a.root, update, false))
			a.Mu.Lock()
			assert.Equal(t, 0, a.firstOpenIndexLocked(a.root))
			a.Mu.Unlock()
			sink.refuse.Store(false)
			require.NoError(t, a.applyUpdate(a.root, update, false))
			a.Mu.Lock()
			if background {
				assert.Equal(t, 1, a.firstOpenIndexLocked(a.root))
			} else {
				assert.Equal(t, 0, a.firstOpenIndexLocked(a.root))
			}
			a.Mu.Unlock()
			require.Len(t, rawSink.Messages(), 1)
			assert.True(t, rawSink.Messages()[0].Stored())
		})
	}
}

func TestAntigravityPersistenceReplayDoesNotSkipUnseenSteps(t *testing.T) {
	a, _ := testAgent(t)
	a.root.steps[1] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true}
	assert.Equal(t, 0, a.firstOpenIndexLocked(a.root))
	a.root.steps[0] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true}
	assert.Equal(t, 2, a.firstOpenIndexLocked(a.root))
	a.root.seededCount = 3
	a.root.steps[4] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true}
	assert.Equal(t, 3, a.firstOpenIndexLocked(a.root))
}

func TestAntigravityPersistenceReleasesCompletedPayload(t *testing.T) {
	a, sink := testAgent(t)
	text := "retained answer " + strings.Repeat("x", 1<<20)
	raw, err := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypePlannerResponse, "status": contracts.AntigravityStepStatusDone,
		"plannerResponse": map[string]any{"modifiedResponse": text, "thinking": "retained reasoning"},
	})
	require.NoError(t, err)
	update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw})
	feedUpdate(t, a, update)
	a.Mu.Lock()
	assert.Zero(t, len(a.root.steps[0].raw))
	assert.Zero(t, len(a.root.steps[0].text))
	a.Mu.Unlock()
	feedUpdate(t, a, update)
	a.Mu.Lock()
	assert.Zero(t, len(a.root.steps[0].raw), "replay must not restore a released payload")
	assert.Zero(t, len(a.root.steps[0].text))
	a.Mu.Unlock()
	messages := transcriptMessages(sink)
	require.Len(t, messages, 2)
	var answer antigravityStep
	require.NoError(t, json.Unmarshal(messages[1].Content, &answer))
	assert.Equal(t, text, answer.PlannerResponse.ModifiedResponse)
}

func TestAntigravityCompletedReplayUpdatesUsageWithoutRestoringPayload(t *testing.T) {
	a, sink := testAgent(t)
	withUsage := func(text string, input, output int) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"type": contracts.AntigravityStepTypePlannerResponse, "status": contracts.AntigravityStepStatusDone,
			"plannerResponse": map[string]any{"modifiedResponse": text},
			"metadata": map[string]any{"modelUsage": map[string]string{
				"inputTokens": fmt.Sprint(input), "outputTokens": fmt.Sprint(output),
			}},
		})
		require.NoError(t, err)
		return raw
	}
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{
		withUsage("first", 5, 1), withUsage("second", 3, 1),
	}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusIdle, []int{0}, []json.RawMessage{
		withUsage("first", 7, 2),
	}))
	messages := sink.Messages()
	require.Len(t, messages, 3)
	require.True(t, messages[2].TurnEnd)
	var frame struct {
		Metadata struct {
			ModelUsage map[string]int64 `json:"modelUsage"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(messages[2].Content, &frame))
	assert.Equal(t, int64(10), frame.Metadata.ModelUsage["inputTokens"])
	assert.Equal(t, int64(3), frame.Metadata.ModelUsage["outputTokens"])
	a.Mu.Lock()
	assert.Zero(t, len(a.root.steps[0].raw))
	assert.Zero(t, len(a.root.steps[0].text))
	a.Mu.Unlock()
}

func TestAntigravityTurnEndRecoversOutputRefusedBeforeAdmission(t *testing.T) {
	for _, raw := range []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "recover final answer"), json.RawMessage(`{"future":{"payload":"recover unknown frame"}}`)} {
		t.Run(string(raw), func(t *testing.T) {
			rawSink := &agenttest.Sink{}
			sink := &transcriptAdmissionSink{ProviderServices: agent.NewProviderServices(rawSink)}
			sink.refuse.Store(true)
			a := newAgentState(sink, antigravityRPC{}, t.TempDir(), nil)
			t.Cleanup(a.streamCancel)
			a.installRoot("cascade", false)
			require.Error(t, a.applyUpdate(a.root, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}), false))
			sink.refuse.Store(false)
			require.NoError(t, a.endTurn(a.root, agent.MessageCompletionInterrupted))
			messages := transcriptMessages(rawSink)
			require.Len(t, messages, 1)
			assert.JSONEq(t, string(raw), string(messages[0].Content))
		})
	}
}
