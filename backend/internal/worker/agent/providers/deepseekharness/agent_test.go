package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOfflineAgent drives the native event path without a model request.
func newOfflineAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := newAgent(agent.NewProviderServices(sink), "/workspace")
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "deepseek-test", ProviderName: "dsh", Ctx: ctx, Cancel: cancel,
		Stdin: agenttest.NopStdin(io.Discard),
	})
	a.sessionID = "native-root"
	a.streams["root"] = &sessionStream{sessionID: a.sessionID, lastSeq: -1,
		pending: map[string][]byte{}, ready: make(chan struct{})}
	return a
}

func TestPublishTurnActiveRaisesItsToken(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestBusyRefusalRepublishesTheTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.active = true
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later turn", nil))
}

func TestRejectsMissingAndReplacedSessionsBeforeTransport(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

func TestTurnFrames(t *testing.T) {
	t.Parallel()
	cases := []agenttest.TurnFrameCase{
		{Name: "native turn start", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/start","seq":1,"time":1000,"data":{"turn":1}}}}`, Moves: true},
		{Name: "native turn end", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/end","seq":1,"time":2000,"data":{"turn":1,"reason":{"kind":"completed"}}}}}`, Moves: true},
		{Name: "running status", Line: `{"type":"item","streamId":"events","value":{"type":"emit","event":"api-session/status","args":["native-root",true]}}`, Moves: true},
		{Name: "idle status before durable end", Line: `{"type":"item","streamId":"events","value":{"type":"emit","event":"api-session/status","args":["native-root",false]}}`},
		{Name: "another session status", Line: `{"type":"item","streamId":"events","value":{"type":"emit","event":"api-session/status","args":["foreign",true]}}`},
		{Name: "assistant output", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"assistant/message","seq":1,"time":1000,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"Answer"}]}}}}}`},
		{Name: "tool call", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"call-1","name":"bash","arguments":"{}"}}}}`},
		{Name: "tool result", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"tool/result","seq":1,"time":1000,"data":{"turn":1,"message":{"role":"tool","toolCallId":"call-1","content":[],"isError":false}}}}}`},
		{Name: "unknown durable event", Line: `{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"future/event","seq":1,"time":1000,"data":{}}}}`},
		{Name: "unknown emit event", Line: `{"type":"item","streamId":"events","value":{"type":"emit","event":"future/event","args":[]}}`},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		require.NoError(t, a.handleFrame([]byte(tc.Line)))
		return sink.TurnActiveCalls
	})
}

func TestIdleStatusCannotReleaseTheQueueBeforeTheDurableEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.active = true
	require.NoError(t, a.handleFrame([]byte(`{"type":"item","streamId":"events","value":{"type":"emit","event":"api-session/status","args":["native-root",false]}}`)))
	assert.True(t, a.active)
	assert.Empty(t, sink.TurnActiveCalls)
	require.NoError(t, a.handleFrame([]byte(`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/end","seq":1,"time":2000,"data":{"turn":1,"reason":{"kind":"completed"}}}}}`)))
	assert.False(t, a.active)
	assert.Equal(t, []string{"turn_end", "turn_active:false"}, sink.TurnLifecycle())
}

func TestRemoteStreamFailureEndsOwnedWorkAndRefusesFurtherNativeInput(t *testing.T) {
	t.Parallel()
	var inputCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inputCalls.Add(1)
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}}}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	base := &agenttest.Sink{}
	a := newOfflineAgent(t, base)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard)})
	a.rpc = remoteRPC{endpoint: endpoint}
	a.active = true
	a.children["native-child"] = &nativeChild{descriptor: nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable"}, agentID: "stored-child", active: true}
	require.NoError(t, a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "native-child", Kind: bgtask.KindSubagent, ChildAgentID: "stored-child", Status: bgtask.StatusRunning}))
	childStream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	a.streams["child"] = childStream
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"root-call","name":"bash","arguments":"{}"}}`)))
	require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"child-call","name":"read","arguments":"{}"}}`)))
	a.controls["native-control"] = nativeControl{agentID: "native-root", sink: a.sink}
	cause := errors.New("the native Remote connection failed")
	a.reportStreamFailure(cause)
	require.Error(t, a.SendInput("This prompt must not reach the disconnected native process.", nil))
	assert.Equal(t, int32(0), inputCalls.Load())
	assert.False(t, a.active)
	assert.False(t, a.children["native-child"].active)
	assert.Empty(t, a.controls)
	assert.Empty(t, a.streams["root"].pending)
	assert.Empty(t, childStream.pending)
	assert.Contains(t, base.ClosedSpans(), "root-call")
	assert.Contains(t, base.Child("stored-child").ClosedSpans(), "child-call")
	task, exists := base.BackgroundTask("native-child")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusFailed, task.Status)
	assert.Equal(t, agent.MessageCompletionError, a.ProcessExitCompletion())
	require.ErrorIs(t, a.Wait(), cause)
}

func TestNativeModelErrorKeepsTheRemoteConnectionAndWaitsForTheDurableTurnEnd(t *testing.T) {
	t.Parallel()
	var inputCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inputCalls.Add(1)
		assert.Equal(t, "/api/session/prompt", r.URL.Path)
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}}}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.rpc = remoteRPC{endpoint: endpoint}
	a.active = true
	frame := []byte(`{"type":"item","streamId":"events","value":{"type":"emit","event":"api-session/error","args":["native-root","The native model request failed."]}}`)
	require.NoError(t, a.handleFrame(frame), "a native model error does not break its healthy Remote transport")
	assert.True(t, a.active, "the model error must not release the queue before its durable turn end")
	assert.Nil(t, a.streamFailure)
	assert.Empty(t, sink.TurnActiveCalls)
	require.NoError(t, a.handleFrame([]byte(`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/end","seq":17,"time":1000,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"The native model request failed.","code":"HTTP_400","status":400}}}}}}`)))
	assert.False(t, a.active)
	require.NoError(t, a.SendInput("Continue after the actual native model failure.", nil))
	assert.Equal(t, int32(1), inputCalls.Load())
}
