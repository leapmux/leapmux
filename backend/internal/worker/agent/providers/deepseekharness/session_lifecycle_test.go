package deepseekharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

// dsh 0.2.1-rc.2 reuses a named Session in `session/create` only while the Session is blank. For a Session that
// started a turn, it mints a new identity, so a resume through `session/create` lost the stored conversation.
func TestCreateSessionResumesAStoredSessionWithoutCreatingOne(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": true, "value": map[string]any{"values": map[string]any{"workingDirectory": "/workspace"}}},
		"session/create":      {"ok": true, "value": map[string]any{"sessionId": "minted"}},
	})
	id, err := a.createSession(context.Background(), "stored")
	require.NoError(t, err)
	assert.Equal(t, "stored", id)
	assert.Equal(t, []string{"session/projections"}, calls.methods())
	assert.Equal(t, map[string]any{"sessionId": "stored"}, calls.first("session/projections")["request"])
}

func TestCreateSessionAcceptsAStoredSessionThatStatesNoDirectory(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": true, "value": map[string]any{"values": map[string]any{}}},
	})
	id, err := a.createSession(context.Background(), "stored")
	require.NoError(t, err)
	assert.Equal(t, "stored", id)
	assert.Equal(t, []string{"session/projections"}, calls.methods())
}

func TestCreateSessionRefusesAStoredSessionOfAnotherDirectory(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": true, "value": map[string]any{"values": map[string]any{"workingDirectory": "/elsewhere"}}},
	})
	_, err := a.createSession(context.Background(), "stored")
	require.ErrorContains(t, err, "another working directory")
	assert.Equal(t, []string{"session/projections"}, calls.methods(), "a refused Session creates nothing")
}

func TestCreateSessionCreatesAnUnstoredSessionUnderItsIdentity(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": true, "value": nil},
		"session/create":      {"ok": true, "value": map[string]any{"sessionId": "stored"}},
	})
	id, err := a.createSession(context.Background(), "stored")
	require.NoError(t, err)
	assert.Equal(t, "stored", id)
	assert.Equal(t, []string{"session/projections", "session/create"}, calls.methods())
	assert.Equal(t, map[string]any{"cwd": "/workspace", "sessionId": "stored"}, calls.first("session/create")["request"])
}

func TestCreateSessionRefusesAnUnstoredSessionThatComesBackUnderAnotherIdentity(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": true, "value": nil},
		"session/create":      {"ok": true, "value": map[string]any{"sessionId": "minted"}},
	})
	_, err := a.createSession(context.Background(), "stored")
	require.ErrorContains(t, err, "invalid Session identity")
}

func TestCreateSessionCreatesANewSessionWithoutReadingTheStore(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/create": {"ok": true, "value": map[string]any{"sessionId": "minted"}},
	})
	id, err := a.createSession(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "minted", id)
	assert.Equal(t, []string{"session/create"}, calls.methods())
	assert.Equal(t, map[string]any{"cwd": "/workspace"}, calls.first("session/create")["request"])
}

func TestCreateSessionReturnsAFailedStoreRead(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	calls := serveNativeReplies(t, a, map[string]map[string]any{
		"session/projections": {"ok": false, "error": map[string]any{"code": "gateway/internal", "message": "the store is unavailable"}},
	})
	_, err := a.createSession(context.Background(), "stored")
	require.ErrorContains(t, err, "the store is unavailable")
	assert.Equal(t, []string{"session/projections"}, calls.methods())
}

func TestResumedSnapshotReconcilesChildrenWithoutSpuriousRevivalAndRevivesOnLiveTurn(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	sink := &agenttest.Sink{}
	connection, opens := serveFollows(t, func(_ int, streamID string) map[string]any {
		return map[string]any{"type": "end", "streamId": streamID}
	})
	a := newOfflineAgent(t, sink)
	a.conn = connection

	// Pre-existing finished child row from before resume must not be revived by historical replay.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "child-finished",
		Kind:   bgtask.KindSubagent,
		Title:  "Finished child",
		Status: bgtask.StatusSucceeded,
	}))

	// Root snapshot announces both a finished child and an incomplete child.
	rootSnap := []byte(`{"type":"item","streamId":"root","value":{"type":"snapshot","cursor":10,"records":[],"hasMore":false,"projections":{"values":{"subagentCatalog":[{"id":"child-finished","createdAt":1000,"mode":"continuable","label":"Finished child"},{"id":"child-incomplete","createdAt":1001,"mode":"continuable","label":"Incomplete child"}]}}}}`)
	require.NoError(t, a.handleFrame(rootSnap))

	streamByChild := map[string]string{}
	for range 2 {
		select {
		case opened := <-opens:
			streamByChild[opened.address.ChildSessionID] = opened.streamID
		case <-ctx.Done():
			t.Fatal("child follow did not open")
		}
	}
	finishedStreamID := streamByChild["child-finished"]
	incompleteStreamID := streamByChild["child-incomplete"]
	require.NotEmpty(t, finishedStreamID)
	require.NotEmpty(t, incompleteStreamID)

	// Replay finished child's historical turn/start + turn/end(completed).
	finishedSnap := []byte(`{"type":"item","streamId":"` + finishedStreamID + `","value":{"type":"snapshot","cursor":3,"records":[{"event":{"type":"subagent/descriptor","seq":1,"time":1000,"data":{"version":0,"mode":"continuable","label":"Finished child"}}},{"event":{"type":"turn/start","seq":2,"time":1001,"data":{"turn":1}}},{"event":{"type":"turn/end","seq":3,"time":1002,"data":{"turn":1,"reason":{"kind":"completed"}}}}],"hasMore":false,"projections":{"values":{"subagent":{"mode":"continuable","label":"Finished child","seq":1}}}}}`)
	require.NoError(t, a.handleFrame(finishedSnap))

	// Replay incomplete child's historical turn/start without turn/end.
	incompleteSnap := []byte(`{"type":"item","streamId":"` + incompleteStreamID + `","value":{"type":"snapshot","cursor":2,"records":[{"event":{"type":"subagent/descriptor","seq":1,"time":1000,"data":{"version":0,"mode":"continuable","label":"Incomplete child"}}},{"event":{"type":"turn/start","seq":2,"time":1001,"data":{"turn":1}}}],"hasMore":false,"projections":{"values":{"subagent":{"mode":"continuable","label":"Incomplete child","seq":1}}}}}`)
	require.NoError(t, a.handleFrame(incompleteSnap))

	assert.Empty(t, sink.RevivedTasks(), "snapshot replay must never call ReviveBackgroundTask")
	finishedRow, ok := sink.BackgroundTask("child-finished")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, finishedRow.Status)
	assert.False(t, a.ActiveChildTurnState("child-finished").Active)

	incompleteRow, ok := sink.BackgroundTask("child-incomplete")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, incompleteRow.Status, "never-ended resumed child must close as unknown outcome")
	assert.False(t, a.ActiveChildTurnState("child-incomplete").Active)

	// Only a live turn/start revives the finished continuable child.
	liveStart := []byte(`{"type":"item","streamId":"` + finishedStreamID + `","value":{"type":"event","event":{"type":"turn/start","seq":4,"time":2000,"data":{"turn":2}}}}`)
	require.NoError(t, a.handleFrame(liveStart))
	assert.Equal(t, []string{"child-finished"}, sink.RevivedTasks())
	finishedRow, ok = sink.BackgroundTask("child-finished")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, finishedRow.Status)
	assert.True(t, a.ActiveChildTurnState("child-finished").Active)
}

func TestChildSnapshotReplaySkipsStaleJobRegisteredEvents(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.streams["child"] = &sessionStream{
		address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: "native-child", Mode: "continuable"},
		sessionID:       "native-child",
		parentSessionID: "native-root",
		childAgentID:    "stored-child",
		lastSeq:         -1,
		pending:         map[string][]byte{},
		ready:           make(chan struct{}),
		resumed:         true,
	}
	snapshot := []byte(`{"type":"item","streamId":"child","value":{"type":"snapshot","cursor":4,"records":[{"event":{"type":"subagent/descriptor","seq":1,"time":1000,"data":{"version":0,"mode":"continuable","label":"Worker"}}},{"event":{"type":"turn/start","seq":2,"time":1001,"data":{"turn":1}}},{"event":{"type":"job/registered","seq":3,"time":1002,"data":{"jobId":"stale-job","command":"sleep 60"}}},{"event":{"type":"turn/end","seq":4,"time":1003,"data":{"turn":1,"reason":{"kind":"completed"}}}}],"hasMore":false,"projections":{"values":{"subagent":{"mode":"continuable","label":"Worker","seq":1}}}}}`)
	require.NoError(t, a.handleFrame(snapshot))

	_, staleFound := sink.Child("stored-child").BackgroundTask("deepseek-job:stale-job")
	assert.False(t, staleFound, "replaying a child snapshot must not open stale StatusRunning shell rows")

	// A live job/registered event after replay still opens its shell row.
	liveJob := []byte(`{"type":"item","streamId":"child","value":{"type":"event","event":{"type":"job/registered","seq":5,"time":2000,"data":{"jobId":"live-job","command":"make test"}}}}`)
	require.NoError(t, a.handleFrame(liveJob))
	row, liveFound := sink.Child("stored-child").BackgroundTask("deepseek-job:live-job")
	require.True(t, liveFound)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}
