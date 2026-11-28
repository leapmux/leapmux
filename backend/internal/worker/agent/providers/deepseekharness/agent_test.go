package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterruptCanCancelCompactionBeforeTheCommandReplies(t *testing.T) {
	a := newOfflineAgent(t, &agenttest.Sink{})
	started, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCommand := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		value := map[string]any{"accepted": true}
		switch r.URL.Path {
		case "/api/commands/execute":
			close(started)
			<-release
			value = map[string]any{"result": map[string]any{"kind": "success"}}
		case "/api/session/cancel":
			close(cancelled)
			releaseCommand()
		default:
			t.Errorf("unexpected native request: %s", r.URL.Path)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": value}}))
	}))
	defer server.Close()
	defer releaseCommand()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a.rpc = remoteRPC{endpoint: endpoint}
	commandDone, stopDone := make(chan error, 1), make(chan error, 1)
	go func() { commandDone <- a.CompactContext() }()
	<-started
	go func() { stopDone <- a.Interrupt(agent.StopContext{}) }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Error("the stop waited for the compaction command that it must cancel")
		releaseCommand()
	}
	require.NoError(t, <-commandDone)
	require.NoError(t, <-stopDone)
}

func TestInterruptRemovesAndRequeuesAPendingSteer(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%t", delivered), func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.active = true
			var mu sync.Mutex
			var methods []string
			var requestID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							Request struct {
								RequestID string `json:"requestId"`
								ItemID    string `json:"itemId"`
								SessionID string `json:"sessionId"`
								Action    struct {
									Kind string `json:"kind"`
								} `json:"action"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
					return
				}
				mu.Lock()
				methods = append(methods, r.URL.Path)
				mu.Unlock()
				input := request.Payload.Args.Request
				if r.URL.Path == "/api/session/prompt" {
					requestID = input.RequestID
					// .tmp/probe/deepseekharness/logs/frames.jsonl, frames 72-79.
					data, err := json.Marshal(map[string]any{"type": "agent/inbox/spliced", "seq": 1, "time": 1000, "data": map[string]any{"inserted": []any{map[string]any{"id": "native-item", "source": map[string]string{"rpcId": requestID}}}}})
					assert.NoError(t, err)
					assert.NoError(t, a.handleSessionEvent(a.streams["root"], data))
					if delivered {
						data, err = json.Marshal(map[string]any{"type": "user/message", "seq": 2, "time": 1000, "data": map[string]any{"source": map[string]string{"rpcId": requestID}}})
						assert.NoError(t, err)
						assert.NoError(t, a.handleSessionEvent(a.streams["root"], data))
					}
				}
				if r.URL.Path == "/api/session/updateQueue" {
					assert.Equal(t, "native-item", input.ItemID)
					assert.Equal(t, "native-root", input.SessionID)
					assert.Equal(t, "remove", input.Action.Kind)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}}}))
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a.rpc = remoteRPC{endpoint: endpoint}
			attachment := &leapmuxv1.Attachment{Filename: "input.png", MimeType: "image/png", Data: []byte{1, 2, 3}}
			require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{InputID: "input-1", Content: "steer", Attachments: []*leapmuxv1.Attachment{attachment}}))
			attachment.Data[0] = 9
			require.NoError(t, a.Interrupt(agent.StopContext{}))
			mu.Lock()
			defer mu.Unlock()
			if delivered {
				assert.Equal(t, []string{"/api/session/prompt", "/api/session/cancel"}, methods)
				assert.Empty(t, sink.RequeuedInputs())
			} else {
				assert.Equal(t, []string{"/api/session/prompt", "/api/session/cancel", "/api/session/updateQueue"}, methods)
				require.Len(t, sink.RequeuedInputs(), 1)
				dropped := sink.RequeuedInputs()[0].Request
				assert.Equal(t, "input-1", dropped.InputID, "the hand-back states the queue item that the Worker steered")
				assert.Equal(t, "steer", dropped.Content)
				require.Len(t, dropped.Attachments, 1)
				assert.Equal(t, []byte{1, 2, 3}, dropped.Attachments[0].Data, "the hand-back keeps the data that the steer carried")
			}
		})
	}
}

func TestRemovePendingSteerKeepsUnconfirmedRemovals(t *testing.T) {
	for _, code := range []string{"", "session/queue-item-not-found", "session/agent-busy", "session/unavailable", "refused"} {
		t.Run(code, func(t *testing.T) {
			base := &agenttest.Sink{}
			a := newOfflineAgent(t, base)
			steer := &pendingSteer{requestID: "request", itemID: "item", request: agent.SteerRequest{InputID: "input-1", Content: "steer"}}
			a.pendingSteers = []*pendingSteer{steer}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID string `json:"rpcId"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
					return
				}
				result := map[string]any{"ok": true, "value": map[string]any{"accepted": code != "refused"}}
				if code != "" && code != "refused" {
					result = map[string]any{"ok": false, "error": map[string]any{"code": code, "message": "native refusal"}}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": result}))
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a.rpc = remoteRPC{endpoint: endpoint}
			err = a.removePendingSteer(steerTarget{sessionID: "native-root", sink: a.sink}, steer)
			switch code {
			case "":
				require.NoError(t, err)
				assert.Empty(t, a.pendingSteers)
				require.Len(t, base.RequeuedInputs(), 1)
			case "session/queue-item-not-found":
				require.NoError(t, err)
				assert.Empty(t, a.pendingSteers)
				assert.Empty(t, base.RequeuedInputs())
			default:
				require.Error(t, err)
				assert.Len(t, a.pendingSteers, 1)
				assert.Empty(t, base.RequeuedInputs())
			}
		})
	}

	t.Run("empty itemID skips updateQueue RPC", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		base := &agenttest.Sink{}
		a := newOfflineAgent(t, base)
		a.rpc = remoteRPC{endpoint: endpoint}
		steer := &pendingSteer{requestID: "request", request: agent.SteerRequest{InputID: "input-1", Content: "steer"}}
		a.pendingSteers = []*pendingSteer{steer}

		require.NoError(t, a.removePendingSteer(steerTarget{sessionID: "native-root", sink: a.sink}, steer))
		assert.Zero(t, calls.Load())
		assert.Len(t, a.pendingSteers, 1)
		assert.Empty(t, base.RequeuedInputs())
	})
}

func TestRemovedSteerCanRetryAQueueFailureWithoutRemovingItAgain(t *testing.T) {
	cause := errors.New("queue write failed")
	base := &agenttest.Sink{RequeueErr: cause}
	a := newOfflineAgent(t, base)
	steer := &pendingSteer{requestID: "request", itemID: "item", request: agent.SteerRequest{InputID: "input-1", Content: "steer"}, removed: true}
	a.pendingSteers = []*pendingSteer{steer}
	require.ErrorIs(t, a.removePendingSteer(steerTarget{sessionID: "native-root", sink: a.sink}, steer), cause)
	assert.Len(t, a.pendingSteers, 1)
	base.RequeueErr = nil
	require.NoError(t, a.removePendingSteer(steerTarget{sessionID: "native-root", sink: a.sink}, steer))
	assert.Empty(t, a.pendingSteers)
	require.Len(t, base.RequeuedInputs(), 1)
	assert.Equal(t, agent.SteerRequest{InputID: "input-1", Content: "steer"}, base.RequeuedInputs()[0].Request,
		"the retry hands back the request that the steer carried")
}

func TestSteerSurvivesTheNativeTurnEnd(t *testing.T) {
	a := newOfflineAgent(t, &agenttest.Sink{})
	assert.True(t, a.SteerSurvivesTurnEnd())
}

// newOfflineAgent drives the native event path without a model request.
func newOfflineAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	return newOfflineAgentOn(t, sink, nil)
}

// newOfflineAgentOn is newOfflineAgent with the clock that times the waits of the agent.
// A nil clock selects the real clock.
func newOfflineAgentOn(t *testing.T, sink *agenttest.Sink, clock quartz.Clock) *Agent {
	t.Helper()
	return newOfflineAgentServing(t, agent.NewProviderServices(sink), clock)
}

// newOfflineAgentServing is newOfflineAgentOn for a test that needs a sink other than the base
// sink, such as agenttest.ControlSink, which records plan updates and published controls.
func newOfflineAgentServing(t *testing.T, services agent.ProviderServices, clock quartz.Clock) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := newAgent(services, "/workspace")
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "deepseek-test", ProviderName: "dsh", Ctx: ctx, Cancel: cancel,
		Stdin: agenttest.NopStdin(io.Discard), Clock: clock,
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
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, agenttest.SendInput(a, "later turn", nil))
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
	require.NoError(t, a.sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:   bgtask.RoleChildTask,
		RowKey: "native-child", Kind: bgtask.KindSubagent, ChildAgentID: "stored-child", Status: bgtask.StatusRunning}))
	childStream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	a.streams["child"] = childStream
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"root-call","name":"bash","arguments":"{}"}}`)))
	require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"child-call","name":"read","arguments":"{}"}}`)))
	a.controls["native-control"] = &nativeControl{agentID: "native-root", sink: a.sink, published: true}
	cause := errors.New("the native Remote connection failed")
	a.reportStreamFailure(cause)
	assert.Equal(t, []map[string]interface{}{{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldError: cause.Error()}}, base.LeapMuxNotifications())
	require.Error(t, agenttest.SendInput(a, "This prompt must not reach the disconnected native process.", nil))
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
	require.NoError(t, agenttest.SendInput(a, "Continue after the actual native model failure.", nil))
	assert.Equal(t, int32(1), inputCalls.Load())
}

func TestDeepSeekIdleInterruptStopAndStreamFailureCloseToolsAndWorkflows(t *testing.T) {
	t.Parallel()

	t.Run("idle interrupt closes root open tools and preserves child stream and bgtask", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		childStream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
		a.streams["child"] = childStream
		require.NoError(t, a.sink.UpsertBackgroundTask(bgtask.Upsert{
			Role: bgtask.RoleChildTask, RowKey: "native-child", Kind: bgtask.KindSubagent, ChildAgentID: "stored-child", Status: bgtask.StatusRunning,
		}))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"turn/start","seq":1,"time":1000,"data":{}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":2,"time":1001,"data":{"callId":"root-call","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"turn/end","seq":3,"time":1002,"data":{"reason":{"kind":"completed"}}}`)))
		require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1001,"data":{"callId":"child-call","name":"read"}}`)))
		require.Empty(t, sink.ClosedSpans())

		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Equal(t, []string{"root-call"}, sink.ClosedSpans())
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
		assert.Empty(t, sink.Child("stored-child").ClosedSpans(), "idle root interrupt must preserve continuable child tools")
		row, exists := sink.BackgroundTask("native-child")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})

	t.Run("Stop closes root and child pending tools and open workflows", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard)})
		childStream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
		a.streams["child"] = childStream
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"root-stop","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"child-stop","name":"read"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool-workflow/run-start","seq":2,"time":1001,"data":{"runId":"wf-1","name":"Build"}}`)))

		a.Stop()
		assert.Contains(t, sink.ClosedSpans(), "root-stop")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
		assert.Contains(t, sink.Child("stored-child").ClosedSpans(), "child-stop")
		assert.Contains(t, sink.Child("stored-child").TurnLifecycle(), "reset_spans")
		wf, exists := sink.BackgroundTask("deepseek-workflow:wf-1")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusStopped, wf.Status)
	})

	t.Run("reportStreamFailure closes open workflows and resets spans and progress", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard)})
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"root-fail","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool-workflow/run-start","seq":2,"time":1001,"data":{"runId":"wf-fail","name":"Deploy"}}`)))

		a.reportStreamFailure(errors.New("connection lost"))
		assert.Contains(t, sink.ClosedSpans(), "root-fail")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
		wf, exists := sink.BackgroundTask("deepseek-workflow:wf-fail")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, wf.Status)
	})

	t.Run("Wait closes tools workflows and active turn when process exits before websocket EOF", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			// Hold the socket open until the client closes it so Process.Wait wins the race.
			for {
				if _, _, err := conn.Read(r.Context()); err != nil {
					return
				}
			}
		}))
		t.Cleanup(server.Close)
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		t.Cleanup(endpoint.Close)

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "deepseek-wait", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
		})
		a.rpc = remoteRPC{endpoint: endpoint}
		require.NoError(t, a.openConnection(a.Context()))

		childStream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
		a.streams["child"] = childStream
		a.active = true
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"root-wait","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(childStream, []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"child-wait","name":"read"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool-workflow/run-start","seq":2,"time":1001,"data":{"runId":"wf-wait","name":"Build"}}`)))

		require.NoError(t, a.Wait())

		assert.Equal(t, []string{"root-wait"}, sink.ClosedSpans())
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.TurnLifecycle(), "turn_active:false")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
		assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("root-wait"))
		assert.False(t, a.active)

		var rootClose agenttest.Message
		for _, msg := range sink.Messages() {
			if msg.SpanID == "root-wait" && msg.Closing {
				rootClose = msg
			}
		}
		assert.Equal(t, agent.MessageCompletionError, rootClose.Completion)

		childSink := sink.Child("stored-child")
		assert.Equal(t, []string{"child-wait"}, childSink.ClosedSpans())
		assert.Contains(t, childSink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, childSink.ProgressUpdates(), agent.ResetProgress())
		assert.Contains(t, childSink.ProgressUpdates(), agent.CompleteOutputProgress("child-wait"))

		wf, exists := sink.BackgroundTask("deepseek-workflow:wf-wait")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, wf.Status)
	})

	t.Run("Wait with nil connection and DiscardOutput still resets spans closes workflows and clears active turn", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "deepseek-wait-discard", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
		})
		a.active = true
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"root-discard","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool-workflow/run-start","seq":2,"time":1001,"data":{"runId":"wf-discard","name":"Lint"}}`)))
		countBeforeExit := sink.MessageCount()
		a.DiscardOutput()

		require.NoError(t, a.Wait())

		assert.Equal(t, countBeforeExit, sink.MessageCount(), "discarding exit must not persist synthetic closing rows")
		assert.Equal(t, []string{"root-discard"}, sink.ClosedSpans())
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.TurnLifecycle(), "turn_active:false")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
		assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("root-discard"))
		assert.False(t, a.active)
		wf, exists := sink.BackgroundTask("deepseek-workflow:wf-discard")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, wf.Status)
	})

	t.Run("Stop followed by Wait runs exit cleanup once and clears active turn", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "deepseek-stop-wait", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
		})
		a.active = true
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool/call","seq":1,"time":1000,"data":{"callId":"root-once","name":"bash"}}`)))
		require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"tool-workflow/run-start","seq":2,"time":1001,"data":{"runId":"wf-once","name":"Test"}}`)))

		a.Stop()
		lifecycleAfterStop := len(sink.TurnLifecycle())
		progressAfterStop := len(sink.ProgressUpdates())
		messagesAfterStop := sink.MessageCount()

		require.NoError(t, a.Wait())

		assert.Equal(t, lifecycleAfterStop, len(sink.TurnLifecycle()), "Wait after Stop must not duplicate teardown lifecycle events")
		assert.Equal(t, progressAfterStop, len(sink.ProgressUpdates()), "Wait after Stop must not duplicate progress resets")
		assert.Equal(t, messagesAfterStop, sink.MessageCount())
		assert.False(t, a.active)
		assert.Contains(t, sink.TurnLifecycle(), "turn_active:false")
		wf, exists := sink.BackgroundTask("deepseek-workflow:wf-once")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusStopped, wf.Status)
	})
}

func TestSendInputClearsOptimisticTurnOnRefusalAndUnstartedFailure(t *testing.T) {
	t.Parallel()

	t.Run("accepted false is a definite refusal and releases active turn", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			accepted := calls.Add(1) > 1
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"accepted": accepted}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SendInput("first prompt", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "explicit accepted=false is a definite refusal")
		assert.False(t, a.active, "definite refusal must clear optimistic active state")
		assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)

		_, err = a.SendInput("second prompt", nil)
		require.NoError(t, err, "next prompt must not fail with ErrAgentBusy")
		assert.Equal(t, int32(2), calls.Load())
	})

	t.Run("HTTP 502 without turn start returns uncertain and clears optimistic active turn", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SendInput("lost response", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.active, "unstarted turn after transport failure must clear optimistic active state")

		_, err = a.SendInput("retry prompt", nil)
		require.NoError(t, err, "agent must not remain stuck busy after unstarted transport failure")
		assert.Equal(t, int32(2), calls.Load())
	})

	t.Run("HTTP 502 after turn start preserves active turn", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}

		done := make(chan error, 1)
		go func() {
			_, err := a.SendInput("started before 502", nil)
			done <- err
		}()
		<-entered
		require.NoError(t, a.handleFrame([]byte(`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/start","seq":1,"time":1000,"data":{"turn":1}}}}`)))
		close(release)

		err = <-done
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.True(t, a.active, "a turn that already started natively must stay active")
	})

	t.Run("HTTP 400 is a definite refusal and clears active turn", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SendInput("bad prompt", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.active)
	})

	t.Run("steer accepted false is a definite refusal and keeps active turn", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"accepted": false}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}
		a.active = true

		_, err = a.SteerInput(agent.SteerRequest{InputID: "steer-1", AttemptID: "att-1", Content: "steer"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.True(t, a.active, "refused steer must not clear the running turn")
	})

	t.Run("promptParts validates nil attachment before uploading earlier files", func(t *testing.T) {
		t.Parallel()
		var uploads atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uploads.Add(1)
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": true, "value": map[string]any{"receiptId": "rec-1"}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		a := newOfflineAgent(t, &agenttest.Sink{})
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.promptParts("native-root", "hi", []*leapmuxv1.Attachment{
			{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
			nil,
		})
		require.Error(t, err)
		assert.Zero(t, uploads.Load(), "nil attachment must fail before uploading any earlier attachment")

		parts, err := a.promptParts("native-root", "hi", []*leapmuxv1.Attachment{
			{Filename: "pic.png", MimeType: "image/png", Data: []byte{1, 2}},
			{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		})
		require.NoError(t, err)
		require.Len(t, parts, 3)
		assert.Equal(t, "image", parts[1]["type"])
		assert.Equal(t, "file", parts[2]["type"])
		assert.Equal(t, "rec-1", parts[2]["receiptId"])
		assert.Equal(t, int32(1), uploads.Load())
	})

	t.Run("promptParts rejects empty file upload receiptId and upload RPC errors", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			if calls.Add(1) == 1 {
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": req.ID,
					"result": map[string]any{"ok": true, "value": map[string]any{"receiptId": ""}},
				}))
				return
			}
			http.Error(w, "upload failed", http.StatusInternalServerError)
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		a := newOfflineAgent(t, &agenttest.Sink{})
		a.rpc = remoteRPC{endpoint: endpoint}
		file := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")}}

		_, err = a.promptParts("native-root", "hi", file)
		require.ErrorContains(t, err, "did not confirm the file upload")

		_, err = a.promptParts("native-root", "hi", file)
		require.Error(t, err)
	})

	t.Run("HTTP 408 is uncertain while remoteFailure is a definite refusal", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"rpcId"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			if calls.Add(1) == 1 {
				http.Error(w, "request timeout", http.StatusRequestTimeout)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"type": "server-response", "rpcId": req.ID,
				"result": map[string]any{"ok": false, "error": map[string]any{"code": "session/refused", "message": "rejected"}},
			}))
		}))
		defer server.Close()
		endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
		require.NoError(t, err)
		defer endpoint.Close()

		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.rpc = remoteRPC{endpoint: endpoint}

		_, err = a.SendInput("timeout prompt", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.active)

		_, err = a.SendInput("refused prompt", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, a.active)
	})

	t.Run("SendInput rechecks state after promptParts and cleans up replySubmission", func(t *testing.T) {
		t.Parallel()
		for _, condition := range []string{"active", "stopped", "streamFailure"} {
			t.Run(condition, func(t *testing.T) {
				t.Parallel()
				entered, release := make(chan struct{}), make(chan struct{})
				var promptCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						ID string `json:"rpcId"`
					}
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
						return
					}
					if r.URL.Path == "/api/fileUploads/upload" {
						close(entered)
						<-release
						assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
							"type": "server-response", "rpcId": req.ID,
							"result": map[string]any{"ok": true, "value": map[string]any{"receiptId": "rec-1"}},
						}))
						return
					}
					promptCalls.Add(1)
					http.Error(w, "should not send prompt", http.StatusInternalServerError)
				}))
				defer server.Close()
				endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
				require.NoError(t, err)
				defer endpoint.Close()

				sink := &agenttest.Sink{}
				a := newOfflineAgent(t, sink)
				a.rpc = remoteRPC{endpoint: endpoint}
				file := []*leapmuxv1.Attachment{{Filename: "doc.txt", MimeType: "text/plain", Data: []byte("body")}}

				done := make(chan error, 1)
				go func() {
					_, err := a.SendInput("with upload", file)
					done <- err
				}()
				<-entered
				switch condition {
				case "active":
					a.Mu.Lock()
					a.active = true
					a.Mu.Unlock()
				case "stopped":
					a.SetStoppedForTest(true)
				case "streamFailure":
					a.Mu.Lock()
					a.streamFailure = errors.New("stream closed")
					a.Mu.Unlock()
				}
				close(release)

				err = <-done
				require.Error(t, err)
				if condition == "active" {
					assert.ErrorIs(t, err, agent.ErrAgentBusy)
				}
				assert.Zero(t, promptCalls.Load())
				a.dispatchMu.Lock()
				assert.Empty(t, a.replySubmissions, "failed SendInput must not leak replySubmissions")
				a.dispatchMu.Unlock()
			})
		}
	})

	t.Run("root SteerInput honors echo and delivered before HTTP 502 error", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			attemptID string
			delivered bool
		}{
			{"tracked steer echoed before 502", "att-1", true},
			{"tracked steer not echoed before 502", "att-2", false},
			{"untracked steer delivered before 502", "", true},
			{"untracked steer not delivered before 502", "", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newOfflineAgent(t, sink)
				a.active = true
				require.NoError(t, a.handleFrame([]byte(`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"turn/start","seq":1,"time":1000,"data":{"turn":5}}}}`)))

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Payload struct {
							Args struct {
								Request struct {
									RequestID string `json:"requestId"`
								} `json:"request"`
							} `json:"args"`
						} `json:"payload"`
					}
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
						return
					}
					if tc.delivered {
						frame := fmt.Sprintf(`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":"user/message","seq":2,"time":1001,"data":{"source":{"kind":"user","rpcId":%q}}}}}`, req.Payload.Args.Request.RequestID)
						assert.NoError(t, a.handleFrame([]byte(frame)))
					}
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer server.Close()
				endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
				require.NoError(t, err)
				defer endpoint.Close()
				a.rpc = remoteRPC{endpoint: endpoint}

				_, err = a.SteerInput(agent.SteerRequest{InputID: "in-1", AttemptID: tc.attemptID, Content: "steer"})
				if tc.delivered {
					require.NoError(t, err)
					if tc.attemptID != "" {
						require.Len(t, sink.SteerResolutions(), 1)
						assert.Equal(t, agent.SteerOutcomeEchoed, sink.SteerResolutions()[0].Outcome)
					}
				} else {
					require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
					assert.Empty(t, sink.SteerResolutions())
				}
				a.Mu.Lock()
				assert.Empty(t, a.pendingSteers)
				a.Mu.Unlock()
				a.dispatchMu.Lock()
				assert.Empty(t, a.replySubmissions)
				a.dispatchMu.Unlock()
			})
		}
	})
}

type failingSteerResolutionSink struct {
	*agenttest.Sink
	err error
}

func (*failingSteerResolutionSink) OutputQueue() *agent.OutputQueue { return nil }

func (s *failingSteerResolutionSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	if s.err != nil {
		err := s.err
		return func() error { return err }
	}
	return s.Sink.CaptureSteerResolution(resolution)
}

func TestSettleDroppedSteerHandlesAttemptFailureAndNilSink(t *testing.T) {
	t.Parallel()

	t.Run("non-retained steer resolution error preserves submission for retry", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("steer resolution write failed")
		base := &agenttest.Sink{}
		failing := &failingSteerResolutionSink{Sink: base, err: cause}
		a := newOfflineAgentServing(t, agent.NewProviderServices(failing), nil)

		steer := &pendingSteer{
			requestID: "req-1",
			itemID:    "item-1",
			request:   agent.SteerRequest{InputID: "in-1", AttemptID: "att-1", Content: "steer"},
			removed:   true,
		}
		a.pendingSteers = []*pendingSteer{steer}
		a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "req-1"}, &steer.request)
		target := steerTarget{sessionID: "native-root", sink: a.sink}

		require.ErrorIs(t, a.removePendingSteer(target, steer), cause)
		assert.Len(t, a.pendingSteers, 1)
		assert.False(t, steer.resolved)
		a.dispatchMu.Lock()
		assert.Contains(t, a.replySubmissions, "req-1")
		a.dispatchMu.Unlock()

		failing.err = nil
		require.NoError(t, a.removePendingSteer(target, steer))
		assert.Empty(t, a.pendingSteers)
		assert.True(t, steer.resolved)
		a.dispatchMu.Lock()
		assert.NotContains(t, a.replySubmissions, "req-1")
		a.dispatchMu.Unlock()
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeDropped,
		}}, base.SteerResolutions())
	})

	t.Run("nil sink settles tracked and untracked steers without panicking", func(t *testing.T) {
		t.Parallel()
		a := newOfflineAgent(t, &agenttest.Sink{})
		tracked := &pendingSteer{
			requestID: "req-tracked",
			itemID:    "item-tracked",
			request:   agent.SteerRequest{InputID: "in-1", AttemptID: "att-1", Content: "tracked"},
			removed:   true,
		}
		untracked := &pendingSteer{
			requestID: "req-untracked",
			itemID:    "item-untracked",
			request:   agent.SteerRequest{InputID: "in-2", Content: "untracked"},
			removed:   true,
		}
		a.pendingSteers = []*pendingSteer{tracked, untracked}
		a.registerReplySubmission(nil, agent.NativeInputReceipt{SessionID: "native-root", InputID: "req-tracked"}, &tracked.request)
		target := steerTarget{sessionID: "native-root", sink: nil}

		require.NoError(t, a.removePendingSteer(target, tracked))
		require.NoError(t, a.removePendingSteer(target, untracked))
		assert.Empty(t, a.pendingSteers)
		assert.True(t, tracked.resolved)
		assert.True(t, untracked.resolved)
		a.dispatchMu.Lock()
		assert.Empty(t, a.replySubmissions)
		a.dispatchMu.Unlock()
	})
}
