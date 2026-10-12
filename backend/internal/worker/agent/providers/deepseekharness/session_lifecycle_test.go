package deepseekharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootSnapshotDoesNotBecomeReadyWhenItsNativeChildCannotBeFollowed(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	raw := []byte(`{"type":"item","streamId":"root","value":{"type":"snapshot","cursor":12,"records":[],"hasMore":false,"projections":{"values":{"subagentCatalog":[{"id":"native-child","createdAt":1000,"mode":"continuable","label":"The native child"}]}}}}`)
	require.ErrorContains(t, a.handleFrame(raw), "stream connection")
	assert.Empty(t, a.childCatalog)
	assert.Empty(t, a.children)
	assert.Equal(t, int64(-1), a.streams["root"].lastSeq)
	select {
	case <-a.streams["root"].ready:
		t.Fatal("the failed native child restoration published root readiness")
	default:
	}
}

func TestChildSnapshotExcludesItsInheritedParentPrefix(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.streams["child"] = &sessionStream{address: sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: "native-child", Mode: "one-shot"}, sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	raw := []byte(`{"type":"item","streamId":"child","value":{"type":"snapshot","cursor":5,"records":[{"event":{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"parent-call","name":"bash","arguments":"{}"}}},{"event":{"type":"user/message","seq":2,"time":1000,"data":{"role":"user","content":[{"type":"text","text":"Inherited parent input."}]}}},{"event":{"type":"subagent/descriptor","seq":3,"time":1000,"data":{"version":0,"mode":"one-shot","label":"The native child"}}},{"event":{"type":"user/message","seq":4,"time":1001,"data":{"role":"user","content":[{"type":"text","text":"The exact child input."}],"source":{"kind":"user"}}}},{"event":{"type":"assistant/message","seq":5,"time":1002,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"The exact child answer."}]}}}}],"hasMore":false,"projections":{"values":{"subagent":{"mode":"one-shot","label":"The native child","seq":3}}}}}`)
	require.NoError(t, a.handleFrame(raw))
	messages := sink.Child("stored-child").Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "The exact child input.")
	assert.Contains(t, string(messages[1].Content), "The exact child answer.")
	assert.Empty(t, a.streams["child"].pending)
}

// serveCommands stands in for `commands/execute` of the native process. It hands each line to
// onLine and answers with the success that the native process gives for a command.
func serveCommands(t *testing.T, a *Agent, onLine func(line string)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/commands/execute", r.URL.Path)
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Line string `json:"line"`
				} `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		onLine(request.Payload.Args.Line)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"commandId": "cmd-1", "result": map[string]any{"kind": "success", "text": "done"}}}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
}

// Source: `@deepseek-ai/dsh` 0.2.0-rc.2, probed on a live Session. `/compact` runs its model request
// inside the `commands/execute` call and emits `compaction/start` and `compaction/end` events,
// but no `turn/start`, no `turn/end`, and no running status. A command that runs no turn gives the
// Worker no event that ends the turn that the input queue opened for the input. The Worker must
// publish the turn state itself, or the queue holds every later input for the life of the process.
func TestCompactionHoldsTheTurnWhileItRunsAndThenReleasesIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(a *Agent) error
	}{
		{"the compact context request", func(a *Agent) error { return a.CompactContext() }},
		{"a typed compact command", func(a *Agent) error { return a.SendInput("/compact", nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			started, release := make(chan struct{}), make(chan struct{})
			serveCommands(t, a, func(line string) {
				assert.Equal(t, "/compact", line)
				close(started)
				<-release
			})
			done := make(chan error, 1)
			go func() { done <- tc.run(a) }()
			<-started
			assert.Equal(t, []string{"turn_active:true"}, sink.TurnLifecycle(), "the agent is busy while the native compaction runs")
			close(release)
			require.NoError(t, <-done)
			assert.Equal(t, []string{"turn_active:true", "turn_active:false"}, sink.TurnLifecycle(), "the end of the compaction releases the turn")
		})
	}
}

func TestFailedCompactionStillReleasesTheTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"commandId": "cmd-1", "result": map[string]any{"kind": "failure", "text": "nothing to compact"}}}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
	require.ErrorContains(t, a.CompactContext(), "nothing to compact")
	assert.Equal(t, []string{"turn_active:true", "turn_active:false"}, sink.TurnLifecycle())
}

func TestCompactionRefusesWhileANativeTurnRuns(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.active = true
	serveCommands(t, a, func(string) { t.Error("the native process must not receive a command while its turn runs") })
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	assert.Empty(t, sink.TurnLifecycle())
}

func TestCommandThatRunsNoTurnPublishesTheTurnState(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"/plan", "/permission read-only", "/goal finish the task"} {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			serveCommands(t, a, func(string) {})
			require.NoError(t, a.SendInput(line, nil))
			assert.Equal(t, []string{"turn_active:false"}, sink.TurnLifecycle(), "a command that starts no turn releases the turn that the input queue opened")
		})
	}
}

func TestCommandDuringARunningNativeTurnKeepsThatTurnActive(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.active = true
	serveCommands(t, a, func(string) {})
	require.NoError(t, a.SteerInput("/plan", nil))
	assert.Equal(t, []string{"turn_active:true"}, sink.TurnLifecycle(), "the native turn still runs")
}
