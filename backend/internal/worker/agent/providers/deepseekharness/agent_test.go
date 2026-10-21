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
			require.NoError(t, a.SteerInput("steer", []*leapmuxv1.Attachment{attachment}))
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
				assert.Equal(t, "steer", sink.RequeuedInputs()[0].Content)
				assert.Equal(t, "dsh-steer:"+requestID, sink.RequeuedInputs()[0].DropID)
				require.Len(t, sink.RequeuedInputs()[0].Attachments, 1)
				assert.Equal(t, []byte{1, 2, 3}, sink.RequeuedInputs()[0].Attachments[0].Data)
			}
		})
	}
}

func TestRemovePendingSteerKeepsUnconfirmedRemovals(t *testing.T) {
	for _, code := range []string{"", "session/queue-item-not-found", "session/unavailable", "refused"} {
		t.Run(code, func(t *testing.T) {
			base := &agenttest.Sink{}
			a := newOfflineAgent(t, base)
			steer := &pendingSteer{requestID: "request", itemID: "item", content: "steer"}
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
			err = a.removePendingSteer("native-root", steer)
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
}

func TestRemovedSteerCanRetryAQueueFailureWithoutRemovingItAgain(t *testing.T) {
	cause := errors.New("queue write failed")
	base := &agenttest.Sink{RequeueErr: cause}
	a := newOfflineAgent(t, base)
	steer := &pendingSteer{requestID: "request", itemID: "item", content: "steer", removed: true}
	a.pendingSteers = []*pendingSteer{steer}
	require.ErrorIs(t, a.removePendingSteer("native-root", steer), cause)
	assert.Len(t, a.pendingSteers, 1)
	base.RequeueErr = nil
	require.NoError(t, a.removePendingSteer("native-root", steer))
	assert.Empty(t, a.pendingSteers)
	assert.Len(t, base.RequeuedInputs(), 1)
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
	a.controls["native-control"] = &nativeControl{agentID: "native-root", sink: a.sink, published: true}
	cause := errors.New("the native Remote connection failed")
	a.reportStreamFailure(cause)
	assert.Equal(t, []map[string]interface{}{{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldError: cause.Error()}}, base.LeapMuxNotifications())
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
