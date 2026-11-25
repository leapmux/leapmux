package deepseekharness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessExactReplyCompletesBeforePromptAcknowledgement(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("transport failure=%t", transportFailure), func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			received := make(chan string, 1)
			release := make(chan struct{})
			releasePrompt := sync.OnceFunc(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							Request struct {
								ID      string `json:"requestId"`
								Session string `json:"sessionId"`
								Mode    string `json:"mode"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				assert.Equal(t, "/api/session/prompt", r.URL.Path)
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				input := request.Payload.Args.Request
				assert.Equal(t, "native-root", input.Session)
				assert.Equal(t, "queue", input.Mode)
				received <- input.ID
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if transportFailure {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": request.ID,
					"result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}},
				}))
			}))
			defer server.Close()
			defer releasePrompt()
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			defer endpoint.Close()
			a.rpc = remoteRPC{endpoint: endpoint, timeout: 30 * time.Second}
			type result struct {
				receipt agent.NativeInputReceipt
				err     error
			}
			done := make(chan result, 1)
			go func() {
				receipt, err := a.SendInput("Check the result.", nil)
				done <- result{receipt, err}
			}()
			var requestID string
			select {
			case requestID = <-received:
			case result := <-done:
				t.Fatalf("the prompt returned before reaching DeepSeek Harness: %v", result.err)
			}
			require.NotEmpty(t, requestID)
			applyReplyEvent(t, a, 1, "turn/start", `{"turn":41}`)
			applyReplyEvent(t, a, 2, "assistant/message", `{"turn":41,"message":{"content":[{"type":"text","text":"Before the echo"}]}}`)
			applyReplyEvent(t, a, 3, "user/message", harnessInputEcho(requestID))
			applyReplyEvent(t, a, 4, "assistant/message", `{"turn":40,"message":{"content":[{"type":"text","text":"Another turn"}]}}`)
			applyReplyEvent(t, a, 5, "assistant/message", `{"turn":41,"message":{"content":[{"type":"reasoning","text":"Reasoning"},{"type":"text","text":"First"},{"type":"text","text":"Second"}]}}`)
			applyReplyEvent(t, a, 5, "assistant/message", `{"turn":41,"message":{"content":[{"type":"text","text":"Replay"}]}}`)
			applyReplyEvent(t, a, 6, "turn/end", `{"turn":41,"reason":{"kind":"completed"}}`)
			require.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "native-root", TurnID: "41", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs: []agent.NativeReplyBoundary{{InputID: requestID, AfterOrdinal: 1}},
				Items: []agent.NativeReplyItem{
					{Ordinal: 2, MessageKey: "session-event:5:block:1"},
					{Ordinal: 3, MessageKey: "session-event:5:block:2"},
				},
			}}, sink.NativeReplyTurns())
			assert.Empty(t, sink.NativeReplyRetirements())
			releasePrompt()
			got := <-done
			require.NoError(t, got.err)
			assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-root", InputID: requestID}, got.receipt)
		})
	}
}

func TestHarnessExactReplyRequiresAProvedEnd(t *testing.T) {
	for _, test := range []struct {
		name       string
		start      string
		output     string
		finalEvent string
		finalData  string
		outcome    leapmuxv1.AgentInputTurnOutcome
		retired    bool
	}{
		{"missing start number", `{}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"completed"}}`, 0, true},
		{"missing output number", `{"turn":8}`, `{"message":{"content":[{"type":"text","text":"Unidentified"}]}}`, "turn/end", `{"turn":8,"reason":{"kind":"completed"}}`, 0, true},
		{"other turn ends", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":7,"reason":{"kind":"completed"}}`, 0, true},
		{"another turn starts", `{"turn":8}`, `{"turn":8}`, "turn/start", `{"turn":9}`, 0, true},
		{"unknown reason", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"future"}}`, 0, true},
		{"interrupted", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"aborted"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, false},
		{"failed", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"error"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			receipt := agent.NativeInputReceipt{SessionID: "native-root", InputID: "request"}
			a.registerReplySubmission(a.sink, receipt, nil)
			applyReplyEvent(t, a, 1, "turn/start", test.start)
			applyReplyEvent(t, a, 2, "user/message", harnessInputEcho(receipt.InputID))
			applyReplyEvent(t, a, 3, "assistant/message", test.output)
			applyReplyEvent(t, a, 4, test.finalEvent, test.finalData)
			if test.retired {
				assert.Empty(t, sink.NativeReplyTurns())
				retirements := sink.NativeReplyRetirements()
				require.Len(t, retirements, 1)
				for input, outcome := range retirements {
					assert.Equal(t, receipt.InputID, input.InputID)
					assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, outcome)
				}
			} else {
				turns := sink.NativeReplyTurns()
				require.Len(t, turns, 1)
				assert.Equal(t, test.outcome, turns[0].Outcome)
				assert.Empty(t, sink.NativeReplyRetirements())
			}
		})
	}
}

func TestHarnessSteerBindsToTheTurnThatActuallyConsumesIt(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	receipt := agent.NativeInputReceipt{SessionID: "native-root", InputID: "native-steer"}
	request := agent.SteerRequest{InputID: "queued-steer", AttemptID: "attempt"}
	applyReplyEvent(t, a, 1, "turn/start", `{"turn":5}`)
	a.registerReplySubmission(a.sink, receipt, &request)
	applyReplyEvent(t, a, 2, "turn/end", `{"turn":5,"reason":{"kind":"completed"}}`)
	assert.Empty(t, sink.SteerResolutions())
	assert.Empty(t, sink.NativeReplyTurns())
	applyReplyEvent(t, a, 3, "turn/start", `{"turn":6}`)
	applyReplyEvent(t, a, 4, "user/message", harnessInputEcho(receipt.InputID))
	receipt.TurnID = "6"
	assert.Equal(t, []agent.SteerResolution{{
		InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: receipt,
	}}, sink.SteerResolutions())
	applyReplyEvent(t, a, 5, "assistant/message", `{"turn":6,"message":{"content":[{"type":"text","text":"This turn consumes the steer."}]}}`)
	applyReplyEvent(t, a, 6, "turn/end", `{"turn":6,"reason":{"kind":"completed"}}`)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:5:block:0"}},
	}}, sink.NativeReplyTurns())
}

type harnessReplyControlSink struct {
	agenttest.ControlSink
	bindings []agent.NativeControlInputs
}

func (s *harnessReplyControlSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	s.bindings = append(s.bindings, binding)
	return nil
}

func TestHarnessExactReplyOwnsControlInEitherStreamOrder(t *testing.T) {
	for _, controlFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("control first=%t", controlFirst), func(t *testing.T) {
			sink := &harnessReplyControlSink{}
			a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
			a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "input"}, nil)
			applyReplyEvent(t, a, 1, "turn/start", `{"turn":8}`)
			applyReplyEvent(t, a, 2, "user/message", harnessInputEcho("input"))
			publishApproval := func(id, callID string) {
				raw := fmt.Sprintf(
					`{"type":"item","streamId":"events","value":{"type":"waterfall","event":%q,"eventId":%q,"agentId":"native-root","request":{"toolName":"bash","callId":%q}}}`,
					contracts.DeepseekHarnessControlEventApproval, id, callID,
				)
				require.NoError(t, a.handleFrame([]byte(raw)))
			}
			publishApproval("unrelated", "other-call")
			if controlFirst {
				publishApproval("owned", "call")
			}
			applyReplyEvent(t, a, 3, "tool/call", `{"turn":8,"name":"bash","callId":"call"}`)
			a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "later-input"}, nil)
			applyReplyEvent(t, a, 4, "user/message", harnessInputEcho("later-input"))
			if !controlFirst {
				publishApproval("owned", "call")
			}
			published := sink.PublishedControls()
			require.Len(t, published, 2)
			assert.Empty(t, published[0].NativeTurnID)
			require.NotEmpty(t, published[1].InstanceID)
			assert.Equal(t, []agent.NativeControlInputs{{
				SessionID: "native-root", RequestID: "owned", InstanceID: published[1].InstanceID, InputIDs: []string{"input"},
			}}, sink.bindings)
		})
	}
}

func applyReplyEvent(t *testing.T, a *Agent, seq int, kind, data string) {
	t.Helper()
	require.NoError(t, a.handleFrame([]byte(fmt.Sprintf(
		`{"type":"item","streamId":"root","value":{"type":"event","event":{"type":%q,"seq":%d,"time":1000,"data":%s}}}`,
		kind, seq, data,
	))))
}

func harnessInputEcho(requestID string) string {
	return fmt.Sprintf(`{"source":{"kind":"user","rpcId":%q}}`, requestID)
}

func TestHarnessChildReplyAndControlOwnership(t *testing.T) {
	rootSink := &harnessReplyControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(rootSink), nil)

	// Register one continuable child and its session stream.
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Child"}
	childID, err := rootSink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID,
		AgentSessionID:   descriptor.ID,
		Title:            descriptor.Label,
	})
	require.NoError(t, err)
	childSink := rootSink.Child(childID)
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: childID}
	a.streams["child"] = &sessionStream{
		address:   sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "continuable"},
		sessionID: descriptor.ID, parentSessionID: "native-root", childAgentID: childID,
		lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{}),
	}

	var receivedRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Request struct {
						RequestID      string `json:"requestId"`
						ChildSessionID string `json:"childSessionId"`
						Delivery       string `json:"delivery"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		assert.Equal(t, "/api/subagents/prompt", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		receivedRequestID = request.Payload.Args.Request.RequestID
		assert.Equal(t, "native-child", request.Payload.Args.Request.ChildSessionID)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"type": "server-response", "rpcId": request.ID,
			"result": map[string]any{"ok": true, "value": map[string]any{"messageId": "msg-child-1"}},
		}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a.rpc = remoteRPC{endpoint: endpoint, timeout: 30 * time.Second}

	result, err := a.SendChildInput("native-child", agent.ChildInput{Content: "Check child."})
	require.NoError(t, err)
	require.Equal(t, agent.NativeInputReceipt{SessionID: "native-child", InputID: receivedRequestID}, result.NativeReceipt)

	applyChildEvent := func(seq int, kind, data string) {
		t.Helper()
		require.NoError(t, a.handleFrame([]byte(fmt.Sprintf(
			`{"type":"item","streamId":"child","value":{"type":"event","event":{"type":%q,"seq":%d,"time":1000,"data":%s}}}`,
			kind, seq, data,
		))))
	}
	applyChildEvent(1, "turn/start", `{"turn":2}`)
	applyChildEvent(2, "user/message", harnessInputEcho(receivedRequestID))
	applyChildEvent(3, "assistant/message", `{"turn":2,"message":{"content":[{"type":"text","text":"Child reply."}]}}`)
	applyChildEvent(4, "turn/end", `{"turn":2,"reason":{"kind":"completed"}}`)

	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "native-child", TurnID: "2",
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receivedRequestID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:3:block:0"}},
	}}, childSink.NativeReplyTurns())
	assert.Empty(t, rootSink.NativeReplyTurns())
}

type harnessRecordedChildControlSink struct {
	*agenttest.Sink
	mu        sync.Mutex
	published []agent.ControlRequest
	bindings  []agent.NativeControlInputs
	children  map[string]*harnessRecordedChildControlSink
}

func newHarnessRecordedChildControlSink(base *agenttest.Sink) *harnessRecordedChildControlSink {
	return &harnessRecordedChildControlSink{
		Sink:     base,
		children: make(map[string]*harnessRecordedChildControlSink),
	}
}

func (s *harnessRecordedChildControlSink) ChildSink(childAgentID string) agent.ProviderServices {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.children[childAgentID]; existing != nil {
		return agent.NewProviderServices(existing)
	}
	wrapped := newHarnessRecordedChildControlSink(s.Child(childAgentID))
	s.children[childAgentID] = wrapped
	return agent.NewProviderServices(wrapped)
}

func (s *harnessRecordedChildControlSink) PublishControlRequest(req agent.ControlRequest) error {
	s.mu.Lock()
	s.published = append(s.published, req)
	s.mu.Unlock()
	return nil
}

func (s *harnessRecordedChildControlSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	s.mu.Lock()
	s.bindings = append(s.bindings, binding)
	s.mu.Unlock()
	return nil
}

func TestHarnessChildSteerAndControlBindToChildSink(t *testing.T) {
	for _, controlFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("control first=%t", controlFirst), func(t *testing.T) {
			base := &agenttest.Sink{}
			rootSink := newHarnessRecordedChildControlSink(base)
			a := newOfflineAgentServing(t, agent.NewProviderServices(rootSink), nil)
			a.permissions = "ask"

			descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Child"}
			childID, err := rootSink.EnsureChildAgent(agent.ChildAgentSpec{
				RegistryRole:     bgtask.RoleChildTask,
				ProviderChildKey: descriptor.ID,
				AgentSessionID:   descriptor.ID,
				Title:            descriptor.Label,
			})
			require.NoError(t, err)
			childBase := rootSink.Child(childID)
			a.childCatalog[descriptor.ID] = descriptor
			a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: childID, active: true}
			a.streams["child"] = &sessionStream{
				address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "continuable"},
				sessionID:       descriptor.ID,
				parentSessionID: "native-root",
				childAgentID:    childID,
				lastSeq:         -1,
				pending:         map[string][]byte{},
				ready:           make(chan struct{}),
			}

			var steerReqID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							Request struct {
								RequestID string `json:"requestId"`
								Delivery  string `json:"delivery"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				assert.Equal(t, "/api/subagents/prompt", r.URL.Path)
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				steerReqID = request.Payload.Args.Request.RequestID
				assert.Equal(t, "steer", request.Payload.Args.Request.Delivery)
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"type": "server-response", "rpcId": request.ID,
					"result": map[string]any{"ok": true, "value": map[string]any{"messageId": "msg-steer-1"}},
				}))
			}))
			defer server.Close()
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			defer endpoint.Close()
			a.rpc = remoteRPC{endpoint: endpoint, timeout: 30 * time.Second}

			applyChildEvent := func(seq int, kind, data string) {
				t.Helper()
				require.NoError(t, a.handleFrame([]byte(fmt.Sprintf(
					`{"type":"item","streamId":"child","value":{"type":"event","event":{"type":%q,"seq":%d,"time":1000,"data":%s}}}`,
					kind, seq, data,
				))))
			}
			applyChildEvent(1, "turn/start", `{"turn":3}`)
			applyChildEvent(2, "assistant/message", `{"turn":3,"message":{"content":[{"type":"text","text":"Before steer."}]}}`)

			_, err = a.SteerChildInput("native-child", agent.SteerRequest{
				InputID: "queue-child-steer", AttemptID: "attempt-1", Content: "Steer child.",
			})
			require.NoError(t, err)
			applyChildEvent(3, "user/message", harnessInputEcho(steerReqID))

			publishChildApproval := func(id, callID string) {
				raw := fmt.Sprintf(
					`{"type":"item","streamId":"events","value":{"type":"waterfall","event":%q,"eventId":%q,"agentId":"native-child","request":{"toolName":"bash","callId":%q}}}`,
					contracts.DeepseekHarnessControlEventApproval, id, callID,
				)
				require.NoError(t, a.handleFrame([]byte(raw)))
			}
			if controlFirst {
				publishChildApproval("child-perm", "child-call-1")
			}
			applyChildEvent(4, "tool/call", `{"turn":3,"name":"bash","callId":"child-call-1"}`)
			if !controlFirst {
				publishChildApproval("child-perm", "child-call-1")
			}
			applyChildEvent(5, "assistant/message", `{"turn":3,"message":{"content":[{"type":"text","text":"After steer."}]}}`)
			applyChildEvent(6, "turn/end", `{"turn":3,"reason":{"kind":"completed"}}`)

			childRecorded := rootSink.children[childID]
			require.NotNil(t, childRecorded)
			assert.Empty(t, rootSink.published)
			assert.Empty(t, rootSink.bindings)
			require.Len(t, childRecorded.published, 1)
			assert.Equal(t, "child-perm", childRecorded.published[0].RequestID)
			require.Equal(t, []agent.NativeControlInputs{{
				SessionID: "native-child", RequestID: "child-perm",
				InstanceID: childRecorded.published[0].InstanceID,
				InputIDs:   []string{steerReqID},
			}}, childRecorded.bindings)

			require.Equal(t, []agent.SteerResolution{{
				InputID: "queue-child-steer", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeEchoed,
				Receipt: agent.NativeInputReceipt{SessionID: "native-child", InputID: steerReqID, TurnID: "3"},
			}}, childBase.SteerResolutions())
			require.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "native-child", TurnID: "3",
				Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs:  []agent.NativeReplyBoundary{{InputID: steerReqID, AfterOrdinal: 1}},
				Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:5:block:0"}},
			}}, childBase.NativeReplyTurns())
		})
	}
}

func TestHarnessChildUncertainDeliveryAndStreamFailureRetirement(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	descriptor := nativeChildDescriptor{ID: "native-child", Parent: "native-root", Mode: "continuable", Label: "Child"}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: descriptor.ID,
		AgentSessionID:   descriptor.ID,
		Title:            descriptor.Label,
	})
	require.NoError(t, err)
	childSink := sink.Child(childID)
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = &nativeChild{descriptor: descriptor, agentID: childID}
	a.streams["child"] = &sessionStream{
		address:         sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: descriptor.ID, Mode: "continuable"},
		sessionID:       descriptor.ID,
		parentSessionID: "native-root",
		childAgentID:    childID,
		lastSeq:         -1,
		pending:         map[string][]byte{},
		ready:           make(chan struct{}),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a.rpc = remoteRPC{endpoint: endpoint, timeout: 30 * time.Second}

	_, err = a.SendChildInput("native-child", agent.ChildInput{Content: "Lost response."})
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)

	// An un-echoed child steer and an active child reply turn both retire cleanly on stream failure.
	a.dispatchMu.Lock()
	a.replySubmissions = map[string]*harnessReplySubmission{
		"steer-req": {
			sink:    a.sink.ChildSink(childID),
			receipt: agent.NativeInputReceipt{SessionID: "native-child", InputID: "steer-req"},
			steer:   agent.SteerRequest{InputID: "queue-steer", AttemptID: "attempt-1"},
		},
	}
	a.replyTurns = map[string]*harnessReplyTurn{
		"native-child": {
			sessionID: "native-child",
			sink:      a.sink.ChildSink(childID),
			id:        "7",
			inputs:    []agent.NativeReplyBoundary{{InputID: "in-flight", AfterOrdinal: 1}},
		},
	}
	a.retireUnfinishedReplies()
	a.dispatchMu.Unlock()

	assert.Equal(t, []agent.SteerResolution{{
		InputID: "queue-steer", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeUncertain,
	}}, childSink.SteerResolutions())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		childSink.NativeReplyRetirements()[agent.NativeInputReceipt{SessionID: "native-child", InputID: "in-flight", TurnID: "7"}])
}

func TestHarnessSendInputForSessionReturnsReceiptAndTracksReply(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	var requestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Request struct {
						ID      string `json:"requestId"`
						Session string `json:"sessionId"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		assert.Equal(t, "/api/session/prompt", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requestID = request.Payload.Args.Request.ID
		assert.Equal(t, "native-root", request.Payload.Args.Request.Session)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"type": "server-response", "rpcId": request.ID,
			"result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}},
		}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a.rpc = remoteRPC{endpoint: endpoint, timeout: 30 * time.Second}

	receipt, err := a.SendInputForSession("native-root", "Check the result.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, requestID)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-root", InputID: requestID}, receipt)

	applyReplyEvent(t, a, 1, "turn/start", `{"turn":12}`)
	applyReplyEvent(t, a, 2, "user/message", harnessInputEcho(requestID))
	applyReplyEvent(t, a, 3, "assistant/message", `{"turn":12,"message":{"content":[{"type":"text","text":"Session reply."}]}}`)
	applyReplyEvent(t, a, 4, "turn/end", `{"turn":12,"reason":{"kind":"completed"}}`)
	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "native-root", TurnID: "12", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: requestID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:3:block:0"}},
	}}, sink.NativeReplyTurns())
}
