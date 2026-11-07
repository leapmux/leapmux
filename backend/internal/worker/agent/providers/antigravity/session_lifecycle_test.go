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

	"github.com/leapmux/leapmux/internal/worker/agent"
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
			a.root.background[4] = backgroundTask{rowKey: "shell", stepIndex: 4}
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
	a.root.background[4] = backgroundTask{rowKey: "shell", stepIndex: 4}
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
	child.background[4] = backgroundTask{rowKey: "child-shell", stepIndex: 4}
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
		RowKey: child.id, ChildAgentID: child.childAgentID, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}))
	require.NoError(t, a.SendChildInput(child.id, "start the child turn", nil))
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
