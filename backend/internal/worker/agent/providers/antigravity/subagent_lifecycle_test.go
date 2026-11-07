package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childLifecycleTestAgent(t *testing.T) (*Agent, *agenttest.Sink, *cascadeState) {
	t.Helper()
	a, sink := testAgent(t)
	child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
	child.parent = a.root
	child.childAgentID = "child-agent"
	child.rowOpen = true
	a.cascades[child.id] = child
	child.connectedOnce.Do(func() { close(child.connected) })
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: child.id, ChildAgentID: child.childAgentID, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}))
	return a, sink, child
}

func TestAntigravityNativeChildWakeRevivesCompletedRow(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)
	require.False(t, row.EndedAt.IsZero())
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	row, _ = sink.BackgroundTask(child.id)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	require.NoError(t, a.endTurn(child, agent.MessageCompletionError))
	row, _ = sink.BackgroundTask(child.id)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.False(t, row.EndedAt.IsZero())
}

func TestAntigravityChildSendReceiptCannotReviveCompletedTurn(t *testing.T) {
	for _, priorRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "first run", true: "later run"}[priorRun], func(t *testing.T) {
			a, sink, child := childLifecycleTestAgent(t)
			if priorRun {
				require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
				require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
			}
			entered := make(chan struct{})
			release, unblock := newTestGate(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				_, _ = w.Write([]byte(`{}`))
			})
			done := make(chan error, 1)
			go func() { done <- a.SendChildInput(child.id, "finish before the receipt", nil) }()
			receiveTestValue(t, entered)
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
			unblock()
			require.NoError(t, receiveTestValue(t, done))
			row, ok := sink.BackgroundTask(child.id)
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.False(t, row.EndedAt.IsZero())
			assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(child.id))
		})
	}
}

func TestAntigravityInterruptedChildCanCompleteItsNextTurn(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	require.NoError(t, a.InterruptChild(child.id, agent.StopContext{}))
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusStopped, row.Status)
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
	row, _ = sink.BackgroundTask(child.id)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.False(t, row.EndedAt.IsZero())
}

type delayedChildCreationSink struct {
	agent.ProviderServices
	childSinkCalls atomic.Int32
	entered        chan struct{}
	release        <-chan struct{}
}

type selectedChildCreationSink struct {
	agent.ProviderServices
	calls        []string
	beforeEnsure func(agent.ChildAgentSpec) error
}

func (s *selectedChildCreationSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	s.calls = append(s.calls, spec.ProviderChildKey)
	if s.beforeEnsure != nil {
		if err := s.beforeEnsure(spec); err != nil {
			return "", err
		}
	}
	return s.ProviderServices.EnsureChildAgent(spec)
}

func TestAntigravityChildSetupPreservesSnapshotRecords(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused peer", true: "replaced record"}[replace], func(t *testing.T) {
			a, sink := testAgent(t)
			a.streamCancel()
			a.root.childKeys["first"] = &childKey{role: "First child"}
			a.root.childKeys["second"] = &childKey{role: "Second child"}
			writer := &selectedChildCreationSink{ProviderServices: a.sink}
			a.root.sink = writer
			var selected, other string
			writer.beforeEnsure = func(spec agent.ChildAgentSpec) error {
				if len(writer.calls) != 1 {
					return nil
				}
				selected = spec.ProviderChildKey
				other = "first"
				if selected == other {
					other = "second"
				}
				a.Mu.Lock()
				if replace {
					a.root.childKeys = map[string]*childKey{selected: {role: "Replacement child"}}
				} else {
					a.root.childKeys[other].refused = true
				}
				a.Mu.Unlock()
				return errors.New("temporary child registration failure")
			}
			a.ensureChildren(a.root)
			a.streamWG.Wait()
			require.Equal(t, []string{selected}, writer.calls, "one pass must not reselect a failed or replaced record")
			assert.Nil(t, a.root.childKeys[selected].state)
			assert.NotContains(t, a.cascades, selected)
			assert.Empty(t, sink.BackgroundTasks())
			writer.calls = nil
			writer.beforeEnsure = nil
			if !replace {
				a.root.childKeys[other].refused = false
			}
			a.ensureChildren(a.root)
			a.streamWG.Wait()
			expected := []string{selected}
			if !replace {
				expected = append(expected, other)
			}
			assert.ElementsMatch(t, expected, writer.calls)
			for _, id := range expected {
				require.NotNil(t, a.root.childKeys[id].state)
				assert.Same(t, a.root.childKeys[id].state, a.cascades[id])
				_, ok := sink.BackgroundTask(id)
				assert.True(t, ok)
			}
		})
	}
}

func (s *delayedChildCreationSink) ChildSink(id string) agent.ProviderServices {
	s.childSinkCalls.Add(1)
	return s.ProviderServices.ChildSink(id)
}

func (s *delayedChildCreationSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	s.entered <- struct{}{}
	<-s.release
	childID, err := s.ProviderServices.EnsureChildAgent(spec)
	if err != nil {
		return "", err
	}
	// The production sink creates this row inside EnsureChildAgent.
	if err := s.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: spec.ProviderChildKey, ChildAgentID: childID, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}); err != nil {
		return "", err
	}
	return childID, nil
}

func TestAntigravityChildSetupCannotEnterReplacementGeneration(t *testing.T) {
	a, sink := testAgent(t)
	release, unblock := newTestGate(t)
	writer := &delayedChildCreationSink{ProviderServices: a.sink, entered: make(chan struct{}, 1), release: release}
	a.sink, a.root.sink = writer, writer
	retired := a.root
	retired.childKeys["late-child"] = &childKey{role: "Retired child"}
	done := make(chan struct{})
	go func() {
		a.ensureChildren(retired)
		close(done)
	}()
	receiveTestValue(t, writer.entered)
	a.Mu.Lock()
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	unblock()
	receiveTestValue(t, done)
	a.streamWG.Wait()
	a.Mu.Lock()
	assert.NotContains(t, a.cascades, "late-child")
	assert.Equal(t, "replacement", a.root.id)
	a.Mu.Unlock()
	assert.Zero(t, writer.childSinkCalls.Load(), "a retired setup must not create a child sink")
	row, ok := sink.BackgroundTask("late-child")
	require.True(t, ok, "the delayed sink created the old row before returning")
	assert.True(t, row.Status.IsFinished(), "a late-created row must not remain running")
	assert.False(t, row.EndedAt.IsZero())
}

type delayedChildUpsertSink struct {
	agent.ProviderServices
	entered chan struct{}
	release <-chan struct{}
}

func (s *delayedChildUpsertSink) UpsertBackgroundTask(value bgtask.Upsert) error {
	s.entered <- struct{}{}
	<-s.release
	return s.ProviderServices.UpsertBackgroundTask(value)
}
func TestAntigravityLateChildUpsertFinishesRetiredRow(t *testing.T) {
	a, sink := testAgent(t)
	release, unblock := newTestGate(t)
	writer := &delayedChildUpsertSink{ProviderServices: a.sink, entered: make(chan struct{}, 1), release: release}
	a.sink = writer
	retired := a.root
	retired.childKeys["late-child"] = &childKey{role: "Retired child"}
	done := make(chan struct{})
	go func() { a.ensureChildren(retired); close(done) }()
	receiveTestValue(t, writer.entered)
	a.Mu.Lock()
	child := a.cascades["late-child"]
	require.NotNil(t, child)
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.closeChildRow(child, bgtask.StatusStopped)
	unblock()
	receiveTestValue(t, done)
	row, ok := sink.BackgroundTask("late-child")
	require.True(t, ok)
	assert.True(t, row.Status.IsFinished())
	assert.False(t, row.EndedAt.IsZero())
}
func TestAntigravityStoredChildInitializationUsesRetiredContext(t *testing.T) {
	a, sink := testAgent(t)
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	var requests atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.ReadAll(r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	})
	parent := a.root
	parent.childKeys["stored-child"] = &childKey{stored: true}
	a.ensureChildren(parent)
	receiveTestValue(t, entered)
	a.Mu.Lock()
	cancel := a.streamCancel
	a.generation++
	a.streamCtx, a.streamCancel = context.WithCancel(context.Background())
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	cancel()
	receiveTestValue(t, canceled)
	done := make(chan struct{})
	go func() { a.streamWG.Wait(); close(done) }()
	receiveTestValue(t, done)
	assert.Equal(t, int32(1), requests.Load())
	assert.Empty(t, transcriptMessages(sink.Child(parent.childKeys["stored-child"].state.childAgentID)))
}

func TestAntigravityChildInputEchoOwnership(t *testing.T) {
	for _, steer := range []bool{false, true} {
		t.Run(map[bool]string{false: "send", true: "steer"}[steer], func(t *testing.T) {
			a, sink, child := childLifecycleTestAgent(t)
			if steer {
				a.beginTurn(child)
			}
			var tags []string
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tags []string `json:"tags"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				tags = request.Tags
				assert.Contains(t, tags, "leapmux:queue-input")
				raw, _ := json.Marshal(map[string]any{"type": "CORTEX_STEP_TYPE_USER_INPUT", "status": "CORTEX_STEP_STATUS_DONE", "userInput": map[string]any{"userResponse": "same text", "tags": tags}})
				assert.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}), false))
				_, _ = w.Write([]byte(`{}`))
			})
			if steer {
				require.NoError(t, a.SteerChildInput(child.id, "same text", nil))
			} else {
				require.NoError(t, a.SendChildInput(child.id, "same text", nil))
			}
			assert.Empty(t, transcriptMessages(sink.Child(child.childAgentID)))
			native := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_USER_INPUT","status":"CORTEX_STEP_STATUS_DONE","userInput":{"userResponse":"same text"}}`)
			for _, index := range []int{1, 1, 2} {
				require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{index}, []json.RawMessage{native}), false))
			}
			assert.Len(t, transcriptMessages(sink.Child(child.childAgentID)), 2)
		})
	}
}
func TestAntigravityVisibleNativeAgentMessagesPersist(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	for index, hidden := range []bool{false, true} {
		raw, _ := json.Marshal(map[string]any{"type": "CORTEX_STEP_TYPE_SYSTEM_MESSAGE", "status": "CORTEX_STEP_STATUS_DONE", "systemMessage": map[string]any{"agentMessage": map[string]any{"id": "message", "sender": "parent", "recipient": "child", "content": "native message", "hideFromUser": hidden}}})
		require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{index}, []json.RawMessage{raw}), false))
	}
	messages := transcriptMessages(sink.Child(child.childAgentID))
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "native message")
	assert.Contains(t, string(messages[0].Content), "parent")
}

func TestAntigravityRetiredFrameStopsAfterDelayedBackgroundWrite(t *testing.T) {
	a, sink := testAgent(t)
	release, unblock := newTestGate(t)
	writer := &delayedChildUpsertSink{ProviderServices: a.sink, entered: make(chan struct{}, 1), release: release}
	retired := a.root
	retired.sink = writer
	var update agentStateUpdate
	require.NoError(t, json.Unmarshal([]byte(`{"status":"CASCADE_RUN_STATUS_RUNNING","backgroundTasksUpdate":{"indices":[0],"totalLength":1,"backgroundTasks":[{"taskSnapshot":{"taskId":"old-shell","stepIndex":2}}]},"contextWindowUsage":{"maxContextTokens":987654,"estimatedTokensUsed":123}}`), &update))
	done := make(chan error, 1)
	go func() { done <- a.applyUpdate(retired, update, false) }()
	receiveTestValue(t, writer.entered)
	a.Mu.Lock()
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.closeBackgroundTasks(retired, bgtask.StatusStopped)
	unblock()
	require.NoError(t, receiveTestValue(t, done))
	row, ok := sink.BackgroundTask("old-shell")
	require.True(t, ok)
	assert.True(t, row.Status.IsFinished())
	assert.Empty(t, sink.SessionInfos(), "no later metadata stage may publish after retirement")
}
func TestAntigravityRetiredRootCannotStopReplacement(t *testing.T) {
	a, _ := testAgent(t)
	exited := make(chan struct{})
	close(exited)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: exited})
	retired := a.root
	a.Mu.Lock()
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.stop(retired)
	assert.False(t, a.IsStopped())
	assert.Equal(t, "replacement", a.root.id)
	assert.NoError(t, a.streamCtx.Err())
}
func TestAntigravityPendingResetKeepsWorkerOwnership(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	a.sessionSwitching = true
	a.fatalCascade(child, errors.New("native stream failed during session/new"))
	assert.True(t, child.closed)
	assert.Error(t, child.connectErr)
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

type antigravityACPTestWriter func([]byte) (int, error)

func (write antigravityACPTestWriter) Write(data []byte) (int, error) { return write(data) }
func TestAntigravityClearCannotInstallAfterConcurrentStop(t *testing.T) {
	a, _ := testAgent(t)
	exited := make(chan struct{})
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: exited})
	var closes atomic.Int32
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	a.SetStdinForTest(agenttest.NopStdin(antigravityACPTestWriter(func(data []byte) (int, error) {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		result := `{}`
		if request.Method == "session/new" {
			result = `{"sessionId":"replacement"}`
		}
		if request.Method == "session/close" && closes.Add(1) == 1 {
			close(entered)
			<-release
		}
		response, _ := json.Marshal(map[string]any{"id": request.ID, "result": json.RawMessage(result)})
		// Process exit can remove the request while this reply waits.
		a.Deliver(request.ID, response)
		return len(data), nil
	})))
	done := make(chan error, 1)
	go func() { _, err := a.ClearContext(); done <- err }()
	receiveTestValue(t, entered)
	close(exited)
	a.Stop()
	unblock()
	assert.Error(t, receiveTestValue(t, done))
	assert.Equal(t, "cascade", a.root.id)
	assert.True(t, a.IsStopped())
}

func TestAntigravityReopenCannotResetANewerAttempt(t *testing.T) {
	a, _, child := childLifecycleTestAgent(t)
	completed := make(chan struct{})
	close(completed)
	child.closed = true
	child.streamDone = make(chan struct{})
	connected := child.connected
	a.Mu.Lock()
	reopened, err := a.reopenChildLocked(child, completed)
	a.Mu.Unlock()
	assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
	assert.False(t, reopened)
	assert.Equal(t, connected, child.connected)
	assert.True(t, child.closed)
}

func setAntigravityACPResponder(t *testing.T, a *Agent, respond func(string) json.RawMessage) {
	t.Helper()
	a.SetStdinForTest(agenttest.NopStdin(antigravityACPTestWriter(func(data []byte) (int, error) {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		response, err := json.Marshal(map[string]any{"id": request.ID, "result": respond(request.Method)})
		if err != nil {
			return 0, err
		}
		if !a.Deliver(request.ID, response) {
			return 0, errors.New("no request waits for the test ACP reply")
		}
		return len(data), nil
	})))
}
func TestAntigravityClearTimeoutCompensatesRetiredSetup(t *testing.T) {
	for _, kind := range []string{"child", "shell"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := testAgent(t)
			clock := testutil.NewQuartzMock(t)
			a.clock = clock
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, antigravityClearTimerTag)
			setAntigravityACPResponder(t, a, func(method string) json.RawMessage {
				if method == "session/new" {
					return json.RawMessage(`{"sessionId":"replacement"}`)
				}
				return json.RawMessage(`{}`)
			})
			entered := make(chan struct{}, 1)
			release, unblock := newTestGate(t)
			replacementStream := make(chan struct{}, 1)
			canceledIDs := make(chan string, 4)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}
				if r.URL.Path == languageServerService+"CancelCascadeInvocation" {
					var request struct {
						CascadeID string `json:"cascadeId"`
					}
					assert.NoError(t, json.Unmarshal(body, &request))
					canceledIDs <- request.CascadeID
					_, _ = w.Write([]byte(`{}`))
					return
				}
				_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)))
				w.(http.Flusher).Flush()
				replacementStream <- struct{}{}
				<-r.Context().Done()
			})
			retired := a.root
			oldGroup := a.streamWG
			rowKey := "late-" + kind
			var work func(context.Context, *cascadeState)
			if kind == "child" {
				retired.sink = &delayedChildCreationSink{ProviderServices: a.sink, entered: entered, release: release}
				retired.childKeys[rowKey] = &childKey{role: "Retired child"}
				work = func(_ context.Context, state *cascadeState) { a.ensureChildren(state) }
			} else {
				retired.sink = &delayedChildUpsertSink{ProviderServices: a.sink, entered: entered, release: release}
				var update agentStateUpdate
				require.NoError(t, json.Unmarshal([]byte(`{"status":"CASCADE_RUN_STATUS_RUNNING","backgroundTasksUpdate":{"indices":[0],"totalLength":1,"backgroundTasks":[{"taskSnapshot":{"taskId":"late-shell","stepIndex":2}}]},"contextWindowUsage":{"maxContextTokens":777}}`), &update))
				work = func(_ context.Context, state *cascadeState) { assert.NoError(t, a.applyUpdate(state, update, false)) }
			}
			t.Cleanup(func() {
				unblock()
				a.Mu.Lock()
				cancel, group := a.streamCancel, a.streamWG
				a.Mu.Unlock()
				cancel()
				group.Wait()
				oldGroup.Wait()
			})
			a.startCascadeWorker(retired, work)
			receiveTestValue(t, entered)
			if kind == "shell" {
				a.Mu.Lock()
				assert.Equal(t, cascadeTurnActive, retired.turnPhase, "RUNNING must be visible before the first blocking effect")
				a.Mu.Unlock()
			}
			cleared := make(chan error, 1)
			go func() { _, err := a.ClearContext(); cleared <- err }()
			ctx := testutil.DeadlineContext(t)
			delay := testutil.WaitForTimer(t, ctx, newTimer)
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
			require.NoError(t, receiveTestValue(t, cleared))
			receiveTestValue(t, replacementStream)
			expected := []string{retired.id}
			if kind == "child" {
				expected = append(expected, rowKey)
			}
			var canceled []string
			for len(canceledIDs) > 0 {
				canceled = append(canceled, <-canceledIDs)
			}
			assert.ElementsMatch(t, expected, canceled)
			unblock()
			finished := make(chan struct{})
			go func() { oldGroup.Wait(); close(finished) }()
			receiveTestValue(t, finished)
			a.Mu.Lock()
			assert.Equal(t, "replacement", a.root.id)
			assert.NotContains(t, a.cascades, rowKey)
			assert.NoError(t, a.streamCtx.Err())
			a.Mu.Unlock()
			row, ok := sink.BackgroundTask(rowKey)
			require.True(t, ok)
			assert.True(t, row.Status.IsFinished())
			assert.False(t, row.EndedAt.IsZero())
			for _, info := range sink.SessionInfos() {
				if usage, ok := info["context_usage"].(map[string]any); ok {
					assert.NotContains(t, usage, "context_window", "the retired frame must not publish its context window")
				}
			}
		})
	}
}
func TestAntigravityConcurrentChildReopenStartsOneAttempt(t *testing.T) {
	a, _, child := childLifecycleTestAgent(t)
	child.closed = true
	child.streamDone = make(chan struct{})
	close(child.streamDone)
	var counts, streams, sends atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		if r.URL.Path == languageServerService+"SendUserCascadeMessage" {
			sends.Add(1)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var request struct {
			InitialStepsPageBounds struct {
				StartIndex int `json:"startIndex"`
			} `json:"initialStepsPageBounds"`
		}
		if !assert.GreaterOrEqual(t, len(body), 5) || !assert.NoError(t, json.Unmarshal(body[5:], &request)) {
			return
		}
		if request.InitialStepsPageBounds.StartIndex == pastEveryStep {
			counts.Add(1)
			_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"mainTrajectoryUpdate":{"stepsUpdate":{"totalLength":0}}}}`)))
			return
		}
		streams.Add(1)
		_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	t.Cleanup(func() { a.streamCancel(); a.streamWG.Wait() })
	rootStarted := make(chan struct{})
	a.startCascadeWorker(a.root, func(ctx context.Context, _ *cascadeState) { close(rootStarted); <-ctx.Done() })
	receiveTestValue(t, rootStarted)
	start := make(chan struct{})
	results := make(chan error, 8)
	var ready sync.WaitGroup
	ready.Add(8)
	for range 8 {
		go func() { ready.Done(); <-start; results <- a.SendChildInput(child.id, "continue once", nil) }()
	}
	ready.Wait()
	close(start)
	successes := 0
	for range 8 {
		err := receiveTestValue(t, results)
		if err == nil {
			successes++
		} else {
			assert.True(t, errors.Is(err, agent.ErrAgentBusy) || errors.Is(err, agent.ErrChildRouteNotReady), "a competing send must receive a retryable admission refusal: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, int32(0), counts.Load())
	assert.Equal(t, int32(1), streams.Load())
	assert.Equal(t, int32(1), sends.Load())
}
func TestAntigravityChildReopenWaitHonorsCancellation(t *testing.T) {
	a, _, child := childLifecycleTestAgent(t)
	child.closed = true
	child.streamDone = make(chan struct{})
	connected := child.connected
	a.streamCancel()
	assert.ErrorIs(t, a.SendChildInput(child.id, "do not send", nil), agent.ErrChildRouteNotReady)
	assert.Equal(t, connected, child.connected)
	assert.True(t, child.closed)
}

func TestAntigravityClearClosesRemovedShellDuringReconciliation(t *testing.T) {
	a, sink := testAgent(t)
	setAntigravityACPResponder(t, a, func(method string) json.RawMessage {
		if method == "session/new" {
			return json.RawMessage(`{"sessionId":"replacement"}`)
		}
		return json.RawMessage(`{}`)
	})
	entered := make(chan struct{}, 1)
	replacementStream := make(chan struct{}, 1)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case languageServerService + "GetCascadeTrajectorySteps":
			entered <- struct{}{}
			<-r.Context().Done()
		case languageServerService + "StreamAgentStateUpdates":
			_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)))
			w.(http.Flusher).Flush()
			replacementStream <- struct{}{}
			<-r.Context().Done()
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	oldGroup := a.streamWG
	t.Cleanup(func() {
		a.Mu.Lock()
		cancel, group := a.streamCancel, a.streamWG
		a.Mu.Unlock()
		cancel()
		group.Wait()
		oldGroup.Wait()
	})
	retired := a.root
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "removed-shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
	retired.background[2] = backgroundTask{rowKey: "removed-shell", stepIndex: 2, slot: 0}
	retired.steps[2] = &stepState{status: "CORTEX_STEP_STATUS_RUNNING"}
	a.beginTurn(retired)
	a.startCascadeWorker(retired, func(_ context.Context, state *cascadeState) {
		assert.NoError(t, a.applyBackgroundTasks(state, nil, true))
	})
	receiveTestValue(t, entered)
	_, err := a.ClearContext()
	require.NoError(t, err)
	receiveTestValue(t, replacementStream)
	assert.Empty(t, retired.background)
	assert.Empty(t, retired.closingBackground)
	row, ok := sink.BackgroundTask("removed-shell")
	require.True(t, ok)
	assert.True(t, row.Status.IsFinished())
	assert.False(t, row.EndedAt.IsZero())
}

type retryBackgroundCloseSink struct {
	agent.ProviderServices
	failNext atomic.Bool
	refuse   atomic.Bool
	closed   chan struct{}
}

func (s *retryBackgroundCloseSink) CloseBackgroundTask(key string, status bgtask.Status) error {
	if s.refuse.Load() || s.failNext.Swap(false) {
		return errors.New("registry storage is unavailable")
	}
	if err := s.ProviderServices.CloseBackgroundTask(key, status); err != nil {
		return err
	}
	s.closed <- struct{}{}
	return nil
}
func TestAntigravityBackgroundCloseRecoversWithoutAnotherFrame(t *testing.T) {
	for _, kind := range []string{"shell", "child"} {
		t.Run(kind, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			sink := &agenttest.Sink{}
			queue := sink.OutputQueue()
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
			writer := &retryBackgroundCloseSink{ProviderServices: agent.NewProviderServices(sink), closed: make(chan struct{}, 1)}
			writer.failNext.Store(true)
			a := newAgentState(writer, antigravityRPC{}, t.TempDir(), clock)
			t.Cleanup(a.streamCancel)
			a.installRoot("cascade", false)
			task := backgroundTask{rowKey: kind, stepIndex: 2}
			if kind == "shell" {
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: task.rowKey, Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
				a.finishBackgroundStep(a.root, task, antigravityStep{Type: "CORTEX_STEP_TYPE_RUN_COMMAND", Status: "CORTEX_STEP_STATUS_DONE"})
			} else {
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: task.rowKey, ChildAgentID: "child-agent", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
				child := newCascadeState(kind, a.sink.ChildSink("child-agent"), a.generation, false)
				child.parent = a.root
				child.rowOpen = true
				a.closeChildRow(child, bgtask.StatusSucceeded)
			}
			require.True(t, queue.HasPending(), "the Worker queue must retain a failed registry close")
			ctx := testutil.DeadlineContext(t)
			delay := testutil.WaitForTimer(t, ctx, newTimer)
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
			receiveTestValue(t, writer.closed)
			queue.StopRetries()
			assert.False(t, queue.HasPending())
			row, ok := sink.BackgroundTask(task.rowKey)
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.False(t, row.EndedAt.IsZero())
		})
	}
}
func TestAntigravityChildReviveFollowsRetainedClose(t *testing.T) {
	sink := &agenttest.Sink{}
	writer := &retryBackgroundCloseSink{ProviderServices: agent.NewProviderServices(sink), closed: make(chan struct{}, 1)}
	writer.refuse.Store(true)
	a := newAgentState(writer, antigravityRPC{}, t.TempDir(), nil)
	t.Cleanup(a.streamCancel)
	a.installRoot("cascade", false)
	child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
	child.parent = a.root
	a.cascades[child.id] = child
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: child.id, ChildAgentID: "child-agent", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
	queue := sink.OutputQueue()
	queue.Append(agent.OutputWrite{Label: "close the previous child turn", Run: func() error { return writer.CloseBackgroundTask(child.id, bgtask.StatusSucceeded) }})
	require.Error(t, queue.Flush())
	a.beginTurn(child)
	writer.refuse.Store(false)
	require.NoError(t, queue.Flush())
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
}

func installClearTestRPC(t *testing.T, a *Agent) (chan string, chan struct{}) {
	t.Helper()
	setAntigravityACPResponder(t, a, func(method string) json.RawMessage {
		if method == "session/new" {
			return json.RawMessage(`{"sessionId":"replacement"}`)
		}
		return json.RawMessage(`{}`)
	})
	canceled := make(chan string, 16)
	opened := make(chan struct{}, 1)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		switch r.URL.Path {
		case languageServerService + "CancelCascadeInvocation":
			var request struct {
				CascadeID string `json:"cascadeId"`
			}
			assert.NoError(t, json.Unmarshal(body, &request))
			canceled <- request.CascadeID
			_, _ = w.Write([]byte(`{}`))
		case languageServerService + "StreamAgentStateUpdates":
			_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)))
			w.(http.Flusher).Flush()
			opened <- struct{}{}
			<-r.Context().Done()
		case languageServerService + "SendUserCascadeMessage":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected native method: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	oldGroup := a.streamWG
	t.Cleanup(func() {
		a.Mu.Lock()
		cancel, group := a.streamCancel, a.streamWG
		a.Mu.Unlock()
		cancel()
		group.Wait()
		oldGroup.Wait()
	})
	return canceled, opened
}
func TestAntigravityClearCancelsAcceptedInputBeforeRunning(t *testing.T) {
	a, _ := testAgent(t)
	canceled, opened := installClearTestRPC(t, a)
	require.NoError(t, a.SendInput("accepted before a stream update", nil))
	a.Mu.Lock()
	assert.Equal(t, cascadeTurnIdle, a.root.turnPhase)
	assert.True(t, a.root.sendInFlight)
	a.Mu.Unlock()
	_, err := a.ClearContext()
	require.NoError(t, err)
	receiveTestValue(t, opened)
	require.Len(t, canceled, 1)
	assert.Equal(t, "cascade", <-canceled)
}
func TestAntigravityClearCancelsKnownOwnedIDsOnce(t *testing.T) {
	a, _, child := childLifecycleTestAgent(t)
	a.root.childKeys[child.id] = &childKey{state: child}
	a.root.childKeys["idle-history"] = &childKey{stored: true, idle: true}
	a.root.childKeys["refused"] = &childKey{refused: true}
	a.beginTurn(child)
	canceled, opened := installClearTestRPC(t, a)
	_, err := a.ClearContext()
	require.NoError(t, err)
	receiveTestValue(t, opened)
	var ids []string
	for len(canceled) > 0 {
		ids = append(ids, <-canceled)
	}
	assert.ElementsMatch(t, []string{"cascade", child.id, "idle-history"}, ids)
}

func TestAntigravityStartupWaitsForStoredChildBaselines(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "child", true: "nested child"}[nested], func(t *testing.T) {
			a, _ := testAgent(t)
			root := a.root
			root.stored = true
			pendingID := "child"
			if nested {
				pendingID = "grandchild"
			}
			entered := make(chan struct{}, 1)
			release, unblock := newTestGate(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) || !assert.GreaterOrEqual(t, len(body), 5) {
					return
				}
				var request struct {
					ConversationID         string `json:"conversationId"`
					InitialStepsPageBounds struct {
						StartIndex int `json:"startIndex"`
					} `json:"initialStepsPageBounds"`
				}
				if !assert.NoError(t, json.Unmarshal(body[5:], &request)) {
					return
				}
				raw := `{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`
				if request.InitialStepsPageBounds.StartIndex == pastEveryStep {
					if request.ConversationID == pendingID {
						entered <- struct{}{}
						<-release
					}
					raw = `{"update":{"mainTrajectoryUpdate":{"stepsUpdate":{"totalLength":2}}}}`
					_, _ = w.Write(encodeSingleFrame([]byte(raw)))
					return
				}
				if request.ConversationID == root.id {
					raw = `{"update":{"status":"CASCADE_RUN_STATUS_IDLE","subtrajectoryUpdates":{"child":{"status":"CASCADE_RUN_STATUS_IDLE"}}}}`
				} else if request.ConversationID == "child" && nested {
					raw = `{"update":{"status":"CASCADE_RUN_STATUS_IDLE","subtrajectoryUpdates":{"grandchild":{"status":"CASCADE_RUN_STATUS_IDLE"}}}}`
				}
				_, _ = w.Write(encodeSingleFrame([]byte(raw)))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			t.Cleanup(func() { unblock(); a.streamCancel(); a.streamWG.Wait() })
			a.startCascadeStream(root)
			receiveTestValue(t, entered)
			receiveTestValue(t, root.connected)
			a.Mu.Lock()
			child := a.cascades["child"]
			a.Mu.Unlock()
			if nested {
				receiveTestValue(t, child.connected)
			}
			a.Mu.Lock()
			pending, err := a.pendingStartupCascadeLocked(root)
			a.Mu.Unlock()
			require.NoError(t, err)
			require.NotNil(t, pending)
			assert.Equal(t, pendingID, pending.id, "startup must wait for the stored descendant's replay boundary")
			unblock()
			require.NoError(t, a.waitForRoot(t.Context(), root, 30*time.Second))
		})
	}
}
func TestAntigravityStartupReportsChildInitializationFailure(t *testing.T) {
	a, _ := testAgent(t)
	a.root.connectedOnce.Do(func() { close(a.root.connected) })
	child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, true)
	child.parent = a.root
	cause := errors.New("native child history is unavailable")
	child.connectErr = cause
	child.closed = true
	child.connectedOnce.Do(func() { close(child.connected) })
	a.cascades[child.id] = child
	a.root.childKeys[child.id] = &childKey{stored: true, state: child}
	assert.ErrorIs(t, a.waitForRoot(t.Context(), a.root, 30*time.Second), cause)
}
func TestAntigravityReopenPreservesEstablishedReplayBoundary(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	a.closeChildRow(child, bgtask.StatusFailed)
	child.closed = true
	child.stored = true
	child.seenUpdate = true
	child.seededCount = 2
	child.streamDone = make(chan struct{})
	close(child.streamDone)
	var recounts atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		if r.URL.Path == languageServerService+"SendUserCascadeMessage" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var request struct {
			InitialStepsPageBounds struct {
				StartIndex int `json:"startIndex"`
			} `json:"initialStepsPageBounds"`
		}
		if !assert.GreaterOrEqual(t, len(body), 5) || !assert.NoError(t, json.Unmarshal(body[5:], &request)) {
			return
		}
		if request.InitialStepsPageBounds.StartIndex == pastEveryStep {
			recounts.Add(1)
			_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"mainTrajectoryUpdate":{"stepsUpdate":{"totalLength":4}}}}`)))
			return
		}
		raw := []byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)
		if request.InitialStepsPageBounds.StartIndex <= 2 {
			raw = []byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE","mainTrajectoryUpdate":{"stepsUpdate":{"indices":[2,3],"totalLength":4,"steps":[{"type":"CORTEX_STEP_TYPE_SYSTEM_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","systemMessage":{"agentMessage":{"content":"new native peer task"}}},{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"modifiedResponse":"new child answer"}}]}}}}`)
		}
		_, _ = w.Write(encodeSingleFrame(raw))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	t.Cleanup(func() { a.streamCancel(); a.streamWG.Wait() })
	require.NoError(t, a.SendChildInput(child.id, "continue after the peer reply", nil))
	assert.Equal(t, int32(0), recounts.Load())
	messages := transcriptMessages(sink.Child(child.childAgentID))
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "new native peer task")
	assert.Contains(t, string(messages[1].Content), "new child answer")
}
func TestAntigravityFatalChildCancelsNativeWork(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle cache", true: "active turn"}[active], func(t *testing.T) {
			a, _, child := childLifecycleTestAgent(t)
			if active {
				a.beginTurn(child)
			} else {
				a.closeChildRow(child, bgtask.StatusSucceeded)
			}
			canceled := make(chan string, 1)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, languageServerService+"CancelCascadeInvocation", r.URL.Path)
				var request struct {
					CascadeID string `json:"cascadeId"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				canceled <- request.CascadeID
				_, _ = w.Write([]byte(`{}`))
			})
			a.fatalCascade(child, errors.New("native child stream cannot be decoded"))
			require.Len(t, canceled, 1)
			assert.Equal(t, child.id, <-canceled)
			assert.False(t, a.root.closed)
			assert.False(t, a.IsStopped())
			assert.NoError(t, a.streamCtx.Err())
		})
	}
}
