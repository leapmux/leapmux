package antigravity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeAntigravityRPC(t *testing.T) (antigravityRPC, func(string) []map[string]any) {
	t.Helper()
	var bodies []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		decoded["method"] = r.URL.Path
		mu.Lock()
		bodies = append(bodies, decoded)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return antigravityRPC{endpoint: endpoint, timeout: time.Second}, func(method string) []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var matches []map[string]any
		for _, body := range bodies {
			if body["method"] == languageServerService+method {
				matches = append(matches, body)
			}
		}
		return matches
	}
}

func TestAntigravitySendInputBuildsStrictRequest(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	require.NoError(t, a.SendInput("hello", nil))
	require.Len(t, requests("SendUserCascadeMessage"), 1)
	body := requests("SendUserCascadeMessage")[0]
	assert.Equal(t, "cascade", body["cascadeId"])
	assert.Equal(t, "/exa.language_server_pb.LanguageServerService/SendUserCascadeMessage", body["method"])
	items := body["items"].([]any)
	assert.Equal(t, "hello", items[0].(map[string]any)["text"])
	assert.Contains(t, body["cascadeConfig"], "plannerConfig")
	a.Mu.Lock()
	assert.True(t, a.root.sendInFlight)
	a.Mu.Unlock()
	assert.ErrorIs(t, a.SendInput("busy", nil), agent.ErrAgentBusy)
}

func TestAntigravitySlashAndCompactInput(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	a.slashCommands = []string{"compact", "plan"}
	require.NoError(t, a.SendInput("/compact", nil))
	items := requests("SendUserCascadeMessage")[0]["items"].([]any)
	assert.Len(t, items, 1)
	assert.Contains(t, items[0], "item")
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.NoError(t, a.CompactContext())
	items = requests("SendUserCascadeMessage")[1]["items"].([]any)
	assert.Len(t, items, 1)
	assert.Contains(t, items[0], "item")
}

func TestAntigravityCompactionFollowsNativeCommandAndCheckpoint(t *testing.T) {
	command := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_USER_INPUT","status":"CORTEX_STEP_STATUS_DONE","metadata":{"executionId":"compact-turn"},"userInput":{"items":[{"item":{"slashCommand":{"info":{"name":"compact","type":"SLASH_COMMAND_TYPE_SYSTEM"}}}}],"tags":["leapmux:queue-input"]}}`)
	checkpoint := func(executionID string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"type":"CORTEX_STEP_TYPE_CHECKPOINT","status":"CORTEX_STEP_STATUS_DONE","metadata":{"executionId":%q},"checkpoint":{"sessionSummary":"Compacted from native context"}}`, executionID))
	}
	for _, completed := range []bool{true, false} {
		t.Run(fmt.Sprintf("checkpoint completes=%t", completed), func(t *testing.T) {
			a, sink := testAgent(t)
			a.sink.AdoptNativeSession(a.root.id)
			emit := func(index int, step json.RawMessage) {
				feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, MainTrajectory: trajectoryPatch{StepsUpdate: stepsPatch{Indices: []int{index}, Steps: []json.RawMessage{step}}}})
			}
			emit(0, command)
			emit(0, command)
			manual := map[string]any{contracts.CompactionFieldTrigger: "manual"}
			assert.Equal(t, []any{manual}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			emit(1, checkpoint("another-execution"))
			assert.Equal(t, []any{manual}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			messages := sink.Messages()
			require.Len(t, messages, 1)
			assert.NotContains(t, string(messages[0].Content), `"trigger":"manual"`)
			if completed {
				emit(2, checkpoint("compact-turn"))
				assert.Equal(t, []any{manual, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
				messages := sink.Messages()
				require.Len(t, messages, 2)
				assert.Contains(t, string(messages[1].Content), `"trigger":"manual"`)
			}
			feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
			assert.Equal(t, []any{manual, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			emit(3, checkpoint("compact-turn"))
			messages = sink.Messages()
			assert.NotContains(t, string(messages[len(messages)-1].Content), `"trigger":"manual"`)
		})
	}
}

func TestAntigravitySteerPolicy(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	assert.ErrorIs(t, a.SteerInput("early", nil), agent.ErrNoActiveTurn)
	a.beginTurn(a.root)
	require.NoError(t, a.SteerInput("steer", nil))
	require.Len(t, requests("SendUserCascadeMessage"), 1)
	assert.True(t, a.SteerSurvivesTurnEnd())
	_, echo := any(a).(agent.SteerEchoReporter)
	assert.False(t, echo)
}

func TestAntigravityInterruptCancelsRootOnly(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, _ := testAgent(t)
	a.rpc = rpc
	child := newCascadeState("child", a.sink.ChildSink("child"), 0, false)
	child.parent = a.root
	a.cascades["child"] = child
	a.beginTurn(a.root)
	a.beginTurn(child)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Len(t, requests("CancelCascadeInvocation"), 1)
	assert.Equal(t, "cascade", requests("CancelCascadeInvocation")[0]["cascadeId"])
	assert.Equal(t, "/exa.language_server_pb.LanguageServerService/CancelCascadeInvocation", requests("CancelCascadeInvocation")[0]["method"])
}

func TestAntigravityInterruptCancelsNativeBackgroundWork(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			rpc, requests := fakeAntigravityRPC(t)
			a, sink := testAgent(t)
			a.rpc = rpc
			if active {
				a.beginTurn(a.root)
			}
			a.root.background["shell"] = backgroundTask{rowKey: "shell", stepIndex: 4}
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			calls := requests("CancelCascadeInvocation")
			require.Len(t, calls, 1)
			assert.Equal(t, "cascade", calls[0]["cascadeId"])
			assert.Equal(t, true, calls[0]["killBackgroundTasks"])
			row, ok := sink.BackgroundTask("shell")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, row.Status, "the native completion must close the task")
		})
	}
}

func TestAntigravityInterruptBackgroundCancellationFailureKeepsTaskOpen(t *testing.T) {
	a, sink := testAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	a.root.background["shell"] = backgroundTask{rowKey: "shell", stepIndex: 4}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
	assert.ErrorContains(t, a.Interrupt(agent.StopContext{}), "cancel the Antigravity turn")
	row, ok := sink.BackgroundTask("shell")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestAntigravityInterruptChildCancelsOnlyItsNativeBackgroundWork(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	rpc, requests := fakeAntigravityRPC(t)
	a.rpc = rpc
	child.background["child-shell"] = backgroundTask{rowKey: "child-shell", stepIndex: 4}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child-shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
	require.NoError(t, a.InterruptChild(child.id, agent.StopContext{}))
	calls := requests("CancelCascadeInvocation")
	require.Len(t, calls, 1)
	assert.Equal(t, child.id, calls[0]["cascadeId"])
	assert.Equal(t, true, calls[0]["killBackgroundTasks"])
}

func TestAntigravitySteerPreservesUncertainDelivery(t *testing.T) {
	for _, childInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", childInput), func(t *testing.T) {
			var accepted atomic.Int32
			a, _ := testAgent(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				_, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				accepted.Add(1)
				w.Header().Set("Content-Length", "5")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			})
			var err error
			if childInput {
				child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
				child.parent = a.root
				a.cascades[child.id] = child
				close(child.connected)
				a.beginTurn(child)
				err = a.SteerChildInput(child.id, "steer once", nil)
			} else {
				a.beginTurn(a.root)
				err = a.SteerInput("steer once", nil)
			}
			assert.Equal(t, int32(1), accepted.Load())
			assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
			assert.NotErrorIs(t, err, agent.ErrAgentBusy)
		})
	}
}

func TestAntigravitySendRefusesRetiredCascade(t *testing.T) {
	for _, childInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", childInput), func(t *testing.T) {
			var delivered atomic.Int32
			a, sink := testAgent(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				delivered.Add(1)
				_, _ = w.Write([]byte(`{}`))
			})
			retired := a.root
			if childInput {
				retired = newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
				retired.parent = a.root
				a.cascades[retired.id] = retired
			}
			a.Mu.Lock()
			a.generation++
			a.installRoot("replacement", false)
			a.Mu.Unlock()
			err := a.sendToStateCommand(retired, "do not send", nil, false, "")
			if childInput {
				assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
			} else {
				assert.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			assert.Zero(t, delivered.Load())
			assert.False(t, retired.sendInFlight)
			assert.Empty(t, sink.TurnActiveCalls)
		})
	}
}

func TestAntigravityPendingChildInputPublishesItsActualSteeringState(t *testing.T) {
	a, sink := testAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	childSink := sink.Child("child-agent")
	child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
	child.parent = a.root
	child.childAgentID = "child-agent"
	child.rowOpen = true
	a.cascades[child.id] = child
	close(child.connected)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask,

		RowKey: child.id, ChildAgentID: child.childAgentID, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}))
	require.NoError(t, agenttest.SendChildInput(a, child.id, agent.ChildInput{Content: "start the child turn"}))
	states := childSink.TurnStates()
	require.NotEmpty(t, states)
	assert.Equal(t, agent.TurnState{Active: true, Steerable: false}, states[len(states)-1])
	assert.Equal(t, a.ActiveChildTurnState(child.id), states[len(states)-1])
	assert.ErrorIs(t, a.SteerChildInput(child.id, "too early", nil), agent.ErrNoActiveTurn)
}

func TestAntigravityTurnSnapshotRequiresAnOpenObserverForSteering(t *testing.T) {
	for _, childSnapshot := range []bool{false, true} {
		for _, scenario := range []string{"closed", "switching", "stale generation", "canceled generation"} {
			t.Run(fmt.Sprintf("child=%t/%s", childSnapshot, scenario), func(t *testing.T) {
				a, _ := testAgent(t)
				state := a.root
				if childSnapshot {
					state = newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
					state.parent = a.root
					a.cascades[state.id] = state
				}
				state.turnPhase = cascadeTurnActive
				switch scenario {
				case "closed":
					state.closed = true
				case "switching":
					a.sessionSwitching = true
				case "stale generation":
					a.generation++
				case "canceled generation":
					a.streamCancel()
				}
				actual := agent.TurnState{}
				if childSnapshot {
					actual = a.ActiveChildTurnState(state.id)
				} else {
					actual = a.PublishTurnActive()
				}
				assert.False(t, actual.Steerable)
				assert.Equal(t, scenario != "stale generation", actual.Active)
			})
		}
	}
}
func TestAntigravityClosedRootDoesNotReportAChildRouteFailure(t *testing.T) {
	a, _ := testAgent(t)
	a.root.closed = true
	err := a.sendToStateCommand(a.root, "do not send", nil, false, "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrChildRouteNotReady)
	assert.Contains(t, err.Error(), "Antigravity")
}

func compactInputStep(execution string) json.RawMessage {
	return json.RawMessage(`{"type":"CORTEX_STEP_TYPE_USER_INPUT","status":"CORTEX_STEP_STATUS_DONE","metadata":{"executionId":"` + execution + `"},"userInput":{"items":[{"item":{"slashCommand":{"info":{"name":"compact","type":"SLASH_COMMAND_TYPE_SYSTEM"}}}}]}}`)
}

func completedCheckpointStep(execution string) json.RawMessage {
	return json.RawMessage(`{"type":"CORTEX_STEP_TYPE_CHECKPOINT","status":"CORTEX_STEP_STATUS_DONE","metadata":{"executionId":"` + execution + `"},"checkpoint":{"sessionSummary":"Summary."}}`)
}

func TestAntigravityDoesNotCorrelateMissingCompactionIDs(t *testing.T) {
	a, sink := testAgent(t)
	a.sink.AdoptNativeSession(a.root.id)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{compactInputStep(""), completedCheckpointStep("")}))
	require.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction), 1)
	rows := transcriptMessages(sink)
	require.Len(t, rows, 1)
	assert.NotContains(t, string(rows[0].Content), `"trigger":"manual"`)
}

func TestAntigravityOrdersCompactionStepsByTrajectoryIndex(t *testing.T) {
	a, sink := testAgent(t)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{1, 0}, []json.RawMessage{completedCheckpointStep("execution"), compactInputStep("execution")}))
	rows := transcriptMessages(sink)
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"trigger":"manual"`)
}

func TestAntigravityPersistsFailedCompactionWithoutCheckpoint(t *testing.T) {
	a, sink := testAgent(t)
	a.sink.AdoptNativeSession(a.root.id)
	failure := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_ERROR_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","errorMessage":{"error":{"shortError":"Summary failed."}}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{compactInputStep("execution"), failure}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	assert.Contains(t, sink.LeapMuxNotifications(), map[string]any{
		contracts.NotificationFieldType:    contracts.NotificationTypeCompactionEnded,
		contracts.NotificationFieldOutcome: contracts.CompactionEndOutcomeFailed,
	})
}

func TestAntigravityClearKeepsOldSessionWhenReplacementStreamIsRefused(t *testing.T) {
	a, sink := testAgent(t)
	var requestsMu sync.Mutex
	var touchedOld bool
	var removed []string
	var sent atomic.Bool
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerService + "StartCascade":
			_, _ = w.Write([]byte(`{"cascadeId":"replacement"}`))
		case languageServerService + "StreamAgentStateUpdates":
			http.Error(w, "The replacement conversation was not found.", http.StatusNotFound)
		case languageServerService + "CancelCascadeInvocation":
			requestsMu.Lock()
			touchedOld = true
			requestsMu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case languageServerService + "ForceStopCascadeTree":
			var request map[string]string
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			requestsMu.Lock()
			removed = append(removed, request["conversationId"])
			requestsMu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case languageServerService + "SendUserCascadeMessage":
			sent.Store(true)
			_, _ = w.Write([]byte(`{}`))
		}
	})
	id, err := a.ClearContext()
	require.Error(t, err)
	assert.Empty(t, id)
	assert.Empty(t, sink.SessionIDs())
	requestsMu.Lock()
	assert.False(t, touchedOld)
	assert.Equal(t, []string{"replacement"}, removed)
	requestsMu.Unlock()
	require.NoError(t, a.SendInputForSession("cascade", "continue here", nil))
	assert.True(t, sent.Load())
}

type delayedCompactionSink struct {
	agent.ProviderServices
	entered chan struct{}
	release <-chan struct{}
}

type delayedCompactionPublication struct {
	agent.TranscriptPublication
	sink *delayedCompactionSink
}

type delayedCompactionOwner struct {
	agent.TranscriptOwner
	sink *delayedCompactionSink
}

func (s *delayedCompactionSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	content = s.ProviderServices.CaptureMessage(content, span)
	if content.Publication != nil {
		content.Publication = delayedCompactionPublication{TranscriptPublication: content.Publication, sink: s}
	}
	return content
}

func (s *delayedCompactionSink) BroadcastSessionInfo(info map[string]any) {
	s.waitBeforeCompactionStart(info)
	s.ProviderServices.BroadcastSessionInfo(info)
}

func (s *delayedCompactionSink) waitBeforeCompactionStart(info map[string]any) {
	if info[contracts.SessionInfoKeyCompaction] != nil {
		s.entered <- struct{}{}
		<-s.release
	}
}

func (p delayedCompactionPublication) Owner() agent.TranscriptOwner {
	return delayedCompactionOwner{TranscriptOwner: p.TranscriptPublication.Owner(), sink: p.sink}
}

func (o delayedCompactionOwner) PublishSessionInfo(info map[string]any) bool {
	o.sink.waitBeforeCompactionStart(info)
	return o.TranscriptOwner.PublishSessionInfo(info)
}

func TestAntigravityRetiredCompactionCannotPublishToReplacement(t *testing.T) {
	a, sink := testAgent(t)
	a.sink.AdoptNativeSession(a.root.id)
	release, unblock := newTestGate(t)
	gated := &delayedCompactionSink{ProviderServices: a.root.sink, entered: make(chan struct{}, 1), release: release}
	old := a.root
	old.sink = gated
	done := make(chan error, 1)
	go func() {
		update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{compactInputStep("execution")})
		done <- a.applyUpdate(old, update, false)
	}()
	receiveTestValue(t, gated.entered)
	a.Mu.Lock()
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.sink.AdoptNativeSession("replacement")
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyCompaction: nil})
	unblock()
	require.NoError(t, receiveTestValue(t, done))
	assert.Equal(t, []any{nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

type compactionNoticeSessionSink struct {
	agent.ProviderServices
	sessionIDs []string
}

func (s *compactionNoticeSessionSink) PersistLeapMuxNotification(info map[string]any) {
	s.sessionIDs = append(s.sessionIDs, s.MessageSessionID())
	s.ProviderServices.PersistLeapMuxNotification(info)
}

func (s *compactionNoticeSessionSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	s.sessionIDs = append(s.sessionIDs, content.AgentSessionID)
	return s.ProviderServices.PersistWorkerNotification(content)
}

func TestAntigravityRetiredCompactionOutcomeKeepsItsSession(t *testing.T) {
	a, _ := testAgent(t)
	old := a.root
	a.sink.AdoptNativeSession(old.id)
	sink := &compactionNoticeSessionSink{ProviderServices: old.sink}
	old.sink = sink
	a.beginManualCompaction(old, "execution")
	a.Mu.Lock()
	a.generation++
	a.installRoot("replacement", false)
	a.Mu.Unlock()
	a.sink.AdoptNativeSession("replacement")
	a.endManualCompaction(old, providerkit.CompactionEndAborted)
	assert.Equal(t, []string{old.id}, sink.sessionIDs)
}

func TestAntigravityIdleInterruptDoesNotLatchInterruptedFlagAndRollbackUsesGen(t *testing.T) {
	t.Parallel()

	t.Run("idle interrupt leaves interrupted flag false and closes idle non-background tool", func(t *testing.T) {
		t.Parallel()
		rpc, _ := fakeAntigravityRPC(t)
		a, sink := testAgent(t)
		a.rpc = rpc
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-1"}},"runCommand":{"command":"sleep 30"}}`)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}))
		feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
		require.Empty(t, sink.ClosedSpans())

		var preserved bool
		require.NoError(t, a.Interrupt(agent.StopContext{}.WithPreserveOpenSpans(func() { preserved = true })))
		assert.False(t, a.root.interrupted, "idle interrupt must not latch root.interrupted for Turn 2")
		assert.False(t, preserved)
		assert.Contains(t, sink.ClosedSpans(), "cmd-1")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
	})

	t.Run("idle interrupt with background task sets PreserveOpenSpans and preserves background tail", func(t *testing.T) {
		t.Parallel()
		rpc, _ := fakeAntigravityRPC(t)
		a, sink := testAgent(t)
		a.rpc = rpc
		a.root.background["shell"] = backgroundTask{rowKey: "shell", stepIndex: 4}

		var preserved bool
		require.NoError(t, a.Interrupt(agent.StopContext{}.WithPreserveOpenSpans(func() { preserved = true })))
		assert.True(t, preserved, "live background shell must set PreserveOpenSpans on idle interrupt")
		assert.NotContains(t, sink.TurnLifecycle(), "reset_spans")
		assert.NotContains(t, sink.ProgressUpdates(), agent.ResetProgress())
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelProgress())
	})

	t.Run("failed active cancel rolls back interrupted flag using interruptGen", func(t *testing.T) {
		t.Parallel()
		a, _ := testAgent(t)
		endpoint, err := providerkit.NewHTTPEndpoint("http://127.0.0.1:1", nil)
		require.NoError(t, err)
		t.Cleanup(endpoint.Close)
		a.rpc = antigravityRPC{endpoint: endpoint, timeout: time.Second}
		a.beginTurn(a.root)
		err = a.Interrupt(agent.StopContext{})
		require.Error(t, err)
		assert.False(t, a.root.interrupted, "failed cancel must roll back root.interrupted")
	})

	t.Run("stale failed cancel does not roll back newer concurrent cancel", func(t *testing.T) {
		t.Parallel()
		a, _ := testAgent(t)
		var calls atomic.Int32
		firstEntered := make(chan struct{})
		releaseFirst, unblockFirst := newTestGate(t)
		a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, languageServerService+"CancelCascadeInvocation", r.URL.Path)
			if calls.Add(1) == 1 {
				close(firstEntered)
				<-releaseFirst
				http.Error(w, "stale cancel failed", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		})
		a.beginTurn(a.root)

		firstErr := make(chan error, 1)
		go func() {
			firstErr <- a.Interrupt(agent.StopContext{})
		}()
		receiveTestValue(t, firstEntered)

		require.NoError(t, a.Interrupt(agent.StopContext{}))
		unblockFirst()
		require.Error(t, receiveTestValue(t, firstErr))

		a.Mu.Lock()
		interrupted := a.root.interrupted
		gen := a.root.interruptGen
		a.Mu.Unlock()
		assert.True(t, interrupted, "stale failed cancel must preserve root.interrupted set by a newer concurrent cancel")
		assert.Equal(t, uint64(2), gen)
	})

	t.Run("concurrent idle interrupt and stream update serialize without racing or duplicate rows", func(t *testing.T) {
		t.Parallel()
		rpc, _ := fakeAntigravityRPC(t)
		a, sink := testAgent(t)
		a.rpc = rpc
		cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-race-0"}},"runCommand":{"command":"sleep 30"}}`)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd}))
		feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})

		doneStep := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","metadata":{"toolCall":{"id":"cmd-race-0"}},"runCommand":{"command":"sleep 30"}}`)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			assert.NoError(t, a.Interrupt(agent.StopContext{}))
		}()
		go func() {
			defer wg.Done()
			assert.NoError(t, a.applyUpdate(a.root, updateWithSteps(cascadeStatusIdle, []int{0}, []json.RawMessage{doneStep}), false))
		}()
		wg.Wait()

		closingCount := 0
		for _, msg := range sink.Messages() {
			if msg.SpanID == "cmd-race-0" && msg.Closing {
				closingCount++
			}
		}
		assert.Equal(t, 1, closingCount, "concurrent idle interrupt and step completion must persist exactly one closing row")
	})
}
