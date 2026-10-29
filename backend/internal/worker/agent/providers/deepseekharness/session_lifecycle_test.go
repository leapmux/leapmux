package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
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

// compactionVerdictOps is the retry operation that the end of one compaction
// turn applies: CompactionEnd's cancellation of the server-error retry.
func compactionVerdictOps() []agenttest.TimelineEntry {
	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.CompactionEnd(), nil)
	return reference.Timeline()
}

// serveCommandReply stands in for `commands/execute` and lets reply write each
// answer, so a test chooses the outcome of the command.
func serveCommandReply(t *testing.T, a *Agent, reply func(w http.ResponseWriter, rpcID string)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		reply(w, request.ID)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
}

// The compaction turn ends when the command returns, and that end applies
// CompactionEnd once, whatever the command's outcome. No compaction retries.
func TestCompactionTurnEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	commandReply := func(kind string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, rpcID string) {
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": rpcID, "result": map[string]any{"ok": true, "value": map[string]any{"commandId": "cmd-1", "result": map[string]any{"kind": kind, "text": "done"}}}}))
		}
	}
	for _, tc := range []struct {
		name    string
		run     func(a *Agent) error
		reply   func(http.ResponseWriter, string)
		success bool
	}{
		{name: "the compact context request", run: func(a *Agent) error { return a.CompactContext() }, reply: commandReply("success"), success: true},
		{name: "a typed compact command", run: func(a *Agent) error { return a.SendInput("/compact", nil) }, reply: commandReply("success"), success: true},
		{name: "a typed compact command with arguments", run: func(a *Agent) error { return a.SendInput("/compact keep the plan", nil) }, reply: commandReply("success"), success: true},
		{name: "a compaction that the native process refuses", run: func(a *Agent) error { return a.CompactContext() }, reply: commandReply("failure")},
		{
			name: "a command request that fails", run: func(a *Agent) error { return a.CompactContext() },
			reply: func(w http.ResponseWriter, _ string) { http.Error(w, "overloaded", http.StatusServiceUnavailable) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			serveCommandReply(t, a, tc.reply)
			err := tc.run(a)
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Equal(t, []string{"turn_active:true", "turn_active:false"}, sink.TurnLifecycle())
		})
	}
}

// A stream failure can end the compaction turn before or after the command
// returns. Either way the compaction applies one verdict, CompactionEnd.
func TestStreamFailureDuringACompactionAppliesOneCompactionVerdict(t *testing.T) {
	t.Parallel()
	for _, failureFirst := range []bool{true, false} {
		t.Run(fmt.Sprint("the failure comes first: ", failureFirst), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			// The failure stops the process. This process has already ended, so
			// the stop returns at once and keeps the command's request alive.
			ended := make(chan struct{})
			close(ended)
			a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: t.Context(), Cancel: func() {}, ProcessDone: ended, Stdin: agenttest.NopStdin(io.Discard)})
			started, release := make(chan struct{}), make(chan struct{})
			serveCommands(t, a, func(string) {
				close(started)
				<-release
			})
			done := make(chan error, 1)
			go func() { done <- a.CompactContext() }()
			<-started
			if failureFirst {
				a.reportStreamFailure(errors.New("the native Remote connection failed"))
				close(release)
				require.NoError(t, <-done)
			} else {
				close(release)
				require.NoError(t, <-done)
				a.reportStreamFailure(errors.New("the native Remote connection failed"))
			}
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, known := sink.LastTurnActive()
			require.True(t, known)
			assert.False(t, active)
		})
	}
}

// A /compact steered into a running turn runs inside that turn, so it ends no
// turn. The running turn keeps its own verdict.
func TestSteeredCompactKeepsTheRunningTurnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	serveCommands(t, a, func(line string) { assert.Equal(t, "/compact keep the plan", line) })
	require.NoError(t, a.SteerInput("/compact keep the plan", nil))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(dshOverloadedEnd)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"}),
		providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The compaction mark belongs to the compaction turn alone. The next turn
// takes the classifier's verdict again.
func TestTurnAfterACompactionTakesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	a.active = false
	serveCommands(t, a, func(string) {})
	require.NoError(t, a.CompactContext())
	root := a.streams["root"]
	require.NoError(t, a.handleSessionEvent(root, []byte(`{"type":"turn/start","seq":2,"time":1793150196000,"data":{"turn":2}}`)))
	require.NoError(t, a.handleSessionEvent(root, []byte(dshOverloadedEnd)))
	want := append(compactionVerdictOps(), providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"})...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
