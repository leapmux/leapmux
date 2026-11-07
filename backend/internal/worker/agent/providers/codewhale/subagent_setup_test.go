package codewhale

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codewhaleSetupFailureSink struct {
	agent.ProviderServices
	stage           string
	released        atomic.Bool
	attempted       chan struct{}
	notify          sync.Once
	ensureSuccess   atomic.Int32
	registrySuccess atomic.Int32
	promptSuccess   atomic.Int32
}

func (sink *codewhaleSetupFailureSink) failure(stage string) error {
	if sink.stage == stage && !sink.released.Load() {
		sink.notify.Do(func() { close(sink.attempted) })
		return errors.New("the child " + stage + " write is unavailable")
	}
	return nil
}
func (sink *codewhaleSetupFailureSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if err := sink.failure("ensure"); err != nil {
		return "", err
	}
	id, err := sink.ProviderServices.EnsureChildAgent(spec)
	if err == nil {
		sink.ensureSuccess.Add(1)
	}
	return id, err
}
func (sink *codewhaleSetupFailureSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	if err := sink.failure("registry"); err != nil {
		return err
	}
	err := sink.ProviderServices.UpsertBackgroundTask(task)
	if err == nil {
		sink.registrySuccess.Add(1)
	}
	return err
}
func (sink *codewhaleSetupFailureSink) PersistChildPrompt(id, prompt string) error {
	if err := sink.failure("prompt"); err != nil {
		return err
	}
	err := sink.ProviderServices.PersistChildPrompt(id, prompt)
	if err == nil {
		sink.promptSuccess.Add(1)
	}
	return err
}

func TestCodewhaleChildSetupRetriesEachRejectedStageOnceItRecovers(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"ensure", "registry", "prompt"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			var barrier *codewhaleSetupFailureSink
			a, sink := newTestAgentWith(t, newFakeRuntime(t), testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
				barrier = &codewhaleSetupFailureSink{ProviderServices: services, stage: stage, attempted: make(chan struct{})}
				return barrier
			}})
			queue := a.sink.OutputQueue()
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			retry := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
			t.Cleanup(retry.Close)
			startChildRun(a)
			require.True(t, queue.HasPending(), "the rejected setup stage must retain its original operation")
			assert.Nil(t, a.children.lookup(testChildID), "the watcher must wait for registry and prompt admission")
			a.startChild(testChildID, "call_spawn", "counter", "A duplicate must not replace the original assignment.")
			testutil.WaitForTimer(t, testutil.DeadlineContext(t), retry)
			barrier.released.Store(true)
			clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
			require.Eventually(t, func() bool { return !queue.HasPending() }, 30*time.Second, time.Millisecond)
			require.NotNil(t, a.children.lookup(testChildID))
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, "child-of-call_spawn", rows[0].ChildAgentID)
			child := sink.Child(rows[0].ChildAgentID)
			require.Len(t, child.Messages(), 1)
			assert.Contains(t, string(child.Messages()[0].Content), "Count the files.")
			assert.NotContains(t, string(child.Messages()[0].Content), "A duplicate")
			assert.EqualValues(t, 1, barrier.ensureSuccess.Load())
			assert.EqualValues(t, 1, barrier.registrySuccess.Load())
			assert.EqualValues(t, 1, barrier.promptSuccess.Load())
		})
	}
}

func TestCodewhaleChildSetupDoesNotStartAWatcherAfterStop(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"ensure", "registry", "prompt"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			var barrier *codewhaleSetupFailureSink
			a, sink := newTestAgentWith(t, newFakeRuntime(t), testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
				barrier = &codewhaleSetupFailureSink{ProviderServices: services, stage: stage, attempted: make(chan struct{})}
				return barrier
			}})
			queue := a.sink.OutputQueue()
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			retry := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
			t.Cleanup(retry.Close)
			startChildRun(a)
			a.Stop()
			barrier.released.Store(true)
			if queue.HasPending() {
				testutil.WaitForTimer(t, testutil.DeadlineContext(t), retry)
				clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
			}
			require.Eventually(t, func() bool { return !queue.HasPending() }, 30*time.Second, time.Millisecond)
			assert.Nil(t, a.children.lookup(testChildID))
			if stage == "ensure" {
				assert.Empty(t, sink.BackgroundTasks())
				assert.Zero(t, barrier.ensureSuccess.Load())
				return
			}
			row, found := sink.BackgroundTask(testChildID)
			require.True(t, found)
			assert.Equal(t, bgtask.StatusStopped, row.Status)
			require.Len(t, sink.Child(row.ChildAgentID).Messages(), 1)
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

type codewhaleBlockingSetupSink struct {
	agent.ProviderServices
	stage   string
	entered chan struct{}
	release chan struct{}
	notify  sync.Once
}

func (sink *codewhaleBlockingSetupSink) block(stage string) {
	if stage == sink.stage {
		sink.notify.Do(func() { close(sink.entered) })
		<-sink.release
	}
}
func (sink *codewhaleBlockingSetupSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	sink.block("ensure")
	return sink.ProviderServices.EnsureChildAgent(spec)
}
func (sink *codewhaleBlockingSetupSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	sink.block("registry")
	return sink.ProviderServices.UpsertBackgroundTask(task)
}
func (sink *codewhaleBlockingSetupSink) PersistChildPrompt(id, prompt string) error {
	sink.block("prompt")
	return sink.ProviderServices.PersistChildPrompt(id, prompt)
}

func TestCodewhaleChildSetupCompletesStoppedStateAfterAnInflightWrite(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"ensure", "registry", "prompt"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			barrier := &codewhaleBlockingSetupSink{stage: stage, entered: make(chan struct{}), release: make(chan struct{})}
			a, sink := newTestAgentWith(t, newFakeRuntime(t), testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
				barrier.ProviderServices = services
				return barrier
			}})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			t.Cleanup(unblock)
			done := make(chan struct{})
			go func() { defer close(done); startChildRun(a) }()
			select {
			case <-barrier.entered:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the child setup write did not start")
			}
			stopped := make(chan struct{})
			go func() { defer close(stopped); a.Stop() }()
			select {
			case <-a.children.ctx.Done():
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the child setup received no stop request")
			}
			unblock()
			select {
			case <-done:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the child setup write did not return")
			}
			select {
			case <-stopped:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the parent stop did not finish")
			}
			require.Eventually(t, func() bool { return !a.sink.OutputQueue().HasPending() }, 30*time.Second, time.Millisecond)
			assert.Nil(t, a.children.lookup(testChildID))
			row, found := sink.BackgroundTask(testChildID)
			require.True(t, found)
			assert.Equal(t, bgtask.StatusStopped, row.Status)
			messages := sink.Child(row.ChildAgentID).Messages()
			require.Len(t, messages, 1)
			assert.True(t, strings.Contains(string(messages[0].Content), "Count the files."))
		})
	}
}

type codewhaleRejectedSetupSink struct {
	agent.ProviderServices
	rejection error
	writes    atomic.Int32
}

func (sink *codewhaleRejectedSetupSink) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", sink.rejection
}
func (sink *codewhaleRejectedSetupSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	sink.writes.Add(1)
	return sink.ProviderServices.UpsertBackgroundTask(task)
}
func (sink *codewhaleRejectedSetupSink) PersistChildPrompt(id, prompt string) error {
	sink.writes.Add(1)
	return sink.ProviderServices.PersistChildPrompt(id, prompt)
}
func (sink *codewhaleRejectedSetupSink) CleanupChildAgent(id string) {
	sink.writes.Add(1)
	sink.ProviderServices.CleanupChildAgent(id)
}

func TestCodewhaleRejectedChildIdentityDoesNotBlockParentOutput(t *testing.T) {
	t.Parallel()
	for name, rejection := range map[string]error{"identity refusal": fmt.Errorf("the identity belongs to another parent: %w", agent.ErrChildIdentityRefused), "empty successful identity": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused := &codewhaleRejectedSetupSink{rejection: rejection}
			a, sink := newTestAgentWith(t, newFakeRuntime(t), testAgentOptions{services: func(services agent.ProviderServices) agent.ProviderServices {
				refused.ProviderServices = services
				return refused
			}})
			startChildRun(a)
			require.False(t, a.sink.OutputQueue().HasPending(), "a permanent identity rejection must finish without a retained retry")
			assert.Zero(t, refused.writes.Load(), "a foreign or missing identity owns no registry, prompt or cleanup writes")
			assert.Nil(t, a.children.lookup(testChildID))
			before := sink.MessageCount()
			parent := itemEvent(20, "item.completed", "parent-answer", contracts.CodewhaleItemKindAgentMessage, "The parent can continue.", nil)
			require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: parent}, agent.SpanInfo{}))
			messages := sink.Messages()
			require.Len(t, messages, before+1)
			assert.Contains(t, string(messages[before].Content), "The parent can continue.")
		})
	}
}

type codewhaleRetainedPromptSink struct {
	agent.ProviderServices
	raw   *agenttest.ControlSink
	child *agenttest.Sink
	calls atomic.Int32
}

func (sink *codewhaleRetainedPromptSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	id, err := sink.ProviderServices.EnsureChildAgent(spec)
	if err == nil {
		sink.child = sink.raw.Child(id)
		// Setup has not admitted the opening prompt or its watcher yet.
		sink.child.PersistErr = errors.New("the opening prompt write is unavailable")
	}
	return id, err
}
func (sink *codewhaleRetainedPromptSink) PersistChildPrompt(id, prompt string) error {
	sink.calls.Add(1)
	return sink.ProviderServices.PersistChildPrompt(id, prompt)
}

func TestCodewhaleChildSetupTransfersAnAcceptedPromptToItsOwnQueue(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{}})
	clock := testutil.NewQuartzMock(t)
	barrier := &codewhaleRetainedPromptSink{}
	a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
		barrier.ProviderServices = services
		return barrier
	}})
	barrier.raw = sink
	startChildRun(a)
	require.NotNil(t, a.children.lookup(testChildID), "the child queue owns the accepted prompt before watcher output can follow it")
	require.NotNil(t, barrier.child)
	assert.False(t, a.sink.OutputQueue().HasPending())
	require.True(t, barrier.child.OutputQueue().HasPending())
	// The private native transcript does not exist and the mock clock has not
	// advanced, so no watcher writes while this controlled storage fault clears.
	barrier.child.PersistErr = nil
	require.NoError(t, barrier.child.OutputQueue().Flush())
	a.startChild(testChildID, "call_spawn", "counter", "A duplicate must not replace the accepted prompt.")
	assert.EqualValues(t, 1, barrier.calls.Load())
	messages := barrier.child.Messages()
	require.Len(t, messages, 2, "one rejected storage attempt and one successful retry use the same admitted prompt")
	for _, message := range messages {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, message.Source)
		assert.Contains(t, string(message.Content), "Count the files.")
		assert.NotContains(t, string(message.Content), "A duplicate")
	}
	assert.False(t, barrier.child.OutputQueue().HasPending())
}
