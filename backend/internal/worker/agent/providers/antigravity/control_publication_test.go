package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type interactionPublicationSink struct {
	*agenttest.ControlSink
	fail    atomic.Bool
	entered chan struct{}
	release <-chan struct{}
	stored  chan agent.ControlRequest
	mu      sync.Mutex
	active  map[string]string
}

func (s *interactionPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	if s.fail.Load() {
		return errors.New("control storage is unavailable")
	}
	if s.entered != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	if err := s.ControlSink.PublishControlRequest(request); err != nil {
		return err
	}
	s.mu.Lock()
	s.active[request.RequestID] = request.InstanceID
	s.mu.Unlock()
	if s.stored != nil {
		s.stored <- request
	}
	return nil
}

func (s *interactionPublicationSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.mu.Lock()
	if s.active[requestID] == instanceID {
		delete(s.active, requestID)
	}
	s.mu.Unlock()
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func publicationTestAgent(t *testing.T, clock quartz.Clock) (*Agent, *interactionPublicationSink) {
	t.Helper()
	sink := &interactionPublicationSink{
		ControlSink: &agenttest.ControlSink{}, active: make(map[string]string), stored: make(chan agent.ControlRequest, 8),
	}
	a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), clock)
	t.Cleanup(a.streamCancel)
	a.installRoot("cascade", false)
	a.root.trajectoryID = "trajectory"
	return a, sink
}

func waitingInteractionStep(kind string) json.RawMessage {
	interaction := `{"permission":{"resource":{"action":"command","target":"echo marker"}}}`
	if kind == "question" {
		interaction = `{"askQuestion":{"questions":[{"question":"Continue?","options":[]}]}}`
	}
	raw, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusWaiting,
		"metadata":             map[string]any{"toolCall": map[string]any{"id": "waiting-call", "name": "run_command"}},
		"requestedInteraction": json.RawMessage(interaction),
	})
	return raw
}

func TestAntigravityControlPublicationRetriesExistingInteraction(t *testing.T) {
	for _, kind := range []string{"permission", "question"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			sink.fail.Store(true)
			update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep(kind)})
			feedUpdate(t, a, update)
			require.Zero(t, sink.PublishedControlCount())
			a.Mu.Lock()
			instance := a.root.controls[0].instanceID
			a.Mu.Unlock()
			sink.fail.Store(false)
			feedUpdate(t, a, update)
			request := receiveTestValue(t, sink.stored)
			assert.Equal(t, instance, request.InstanceID)
			feedUpdate(t, a, update)
			assert.Equal(t, 1, sink.PublishedControlCount())
		})
	}
}

func TestAntigravityControlPublicationCannotOutliveRetirement(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "withdraw", true: "retire all"}[bulk], func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			sink.entered = make(chan struct{}, 1)
			var unblock func()
			sink.release, unblock = newTestGate(t)
			done := make(chan struct{})
			go func() {
				_ = a.applyUpdate(a.root, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}), false)
				close(done)
			}()
			receiveTestValue(t, sink.entered)
			if bulk {
				a.retireAllInteractions(a.root)
			} else {
				a.withdrawInteraction(a.root, 0)
			}
			unblock()
			receiveTestValue(t, done)
			a.Mu.Lock()
			assert.Empty(t, a.root.controls)
			a.Mu.Unlock()
			sink.mu.Lock()
			assert.Empty(t, sink.active)
			sink.mu.Unlock()
			assert.Len(t, sink.CanceledControls(), 1)
		})
	}
}

func TestAntigravityControlPublicationRecoversWithoutAnotherFrame(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink := publicationTestAgent(t, clock)
	queue := sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	sink.fail.Store(true)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	require.True(t, queue.HasPending(), "publication must retain its retry without another native frame")
	sink.fail.Store(false)
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	request := receiveTestValue(t, sink.stored)
	queue.StopRetries()
	assert.Equal(t, "antigravity:cascade:trajectory:0", request.RequestID)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.False(t, queue.HasPending())
}

func TestAntigravityPlanPublicationRecoversWithoutAnotherTurn(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink := publicationTestAgent(t, clock)
	queue := sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	a.mode = contracts.AntigravityModePlan
	a.homeDir = t.TempDir()
	path := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", a.root.id, "implementation_plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("# Retained plan\n\nVerify this plan.\n"), 0o600))
	a.beginTurn(a.root)
	a.root.planPath = path
	sink.fail.Store(true)
	require.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
	require.True(t, queue.HasPending(), "a failed plan publication must remain available for retry")
	sink.fail.Store(false)
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	request := receiveTestValue(t, sink.stored)
	queue.StopRetries()
	assert.Contains(t, string(request.Payload), contracts.AntigravityControlToolPlanReview)
	assert.Contains(t, string(request.Payload), "Verify this plan.")
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, 1, sink.PlanUpdateCount())
	assert.False(t, queue.HasPending())
}
