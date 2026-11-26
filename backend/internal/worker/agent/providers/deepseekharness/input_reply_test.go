package deepseekharness

import (
	"encoding/json"
	"fmt"
	"io"
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
		{"max-tokens", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"max-tokens"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, false},
		{"interrupted via aborted", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"aborted"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, false},
		{"interrupted via interrupted", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"interrupted"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, false},
		{"failed via error", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"error"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, false},
		{"failed via blocked", `{"turn":8}`, `{"turn":8}`, "turn/end", `{"turn":8,"reason":{"kind":"blocked"}}`, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, false},
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

func TestHarnessExactReplyOwnsControlInEitherStreamOrder(t *testing.T) {
	for _, controlFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("control first=%t", controlFirst), func(t *testing.T) {
			base := &agenttest.Sink{}
			sink := newHarnessRecordedChildControlSink(base)
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
			sink.mu.Lock()
			published := append([]agent.ControlRequest(nil), sink.published...)
			sink.mu.Unlock()
			require.Len(t, published, 2)
			assert.Empty(t, published[0].NativeTurnID)
			assert.Empty(t, published[0].NativeInputIDs)
			require.NotEmpty(t, published[1].InstanceID)
			if controlFirst {
				assert.Empty(t, published[1].NativeInputIDs)
				assert.Equal(t, []agent.NativeControlInputs{{
					SessionID: "native-root", RequestID: "owned", InstanceID: published[1].InstanceID, InputIDs: []string{"input"},
				}}, sink.NativeControlInputBindings())
			} else {
				assert.Equal(t, []string{"input"}, published[1].NativeInputIDs)
				assert.Empty(t, sink.NativeControlInputBindings())
			}
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
	rootSink := &agenttest.ControlSink{}
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

	var mu sync.Mutex
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
		mu.Lock()
		receivedRequestID = request.Payload.Args.Request.RequestID
		mu.Unlock()
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
	mu.Lock()
	gotRequestID := receivedRequestID
	mu.Unlock()
	require.Equal(t, agent.NativeInputReceipt{SessionID: "native-child", InputID: gotRequestID}, result.NativeReceipt)

	applyChildEvent := func(seq int, kind, data string) {
		t.Helper()
		require.NoError(t, a.handleFrame([]byte(fmt.Sprintf(
			`{"type":"item","streamId":"child","value":{"type":"event","event":{"type":%q,"seq":%d,"time":1000,"data":%s}}}`,
			kind, seq, data,
		))))
	}
	applyChildEvent(1, "turn/start", `{"turn":2}`)
	applyChildEvent(2, "user/message", harnessInputEcho(gotRequestID))
	applyChildEvent(3, "assistant/message", `{"turn":2,"message":{"content":[{"type":"text","text":"Child reply."}]}}`)
	applyChildEvent(4, "turn/end", `{"turn":2,"reason":{"kind":"completed"}}`)

	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "native-child", TurnID: "2",
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: gotRequestID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:3:block:0"}},
	}}, childSink.NativeReplyTurns())
	assert.Empty(t, rootSink.NativeReplyTurns())
}

type harnessRecordedChildControlSink struct {
	*agenttest.Sink
	mu        sync.Mutex
	published []agent.ControlRequest
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

			var steerMu sync.Mutex
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
				steerMu.Lock()
				steerReqID = request.Payload.Args.Request.RequestID
				steerMu.Unlock()
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
			steerMu.Lock()
			gotSteerReqID := steerReqID
			steerMu.Unlock()
			applyChildEvent(3, "user/message", harnessInputEcho(gotSteerReqID))

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
			assert.Empty(t, rootSink.NativeControlInputBindings())
			require.Len(t, childRecorded.published, 1)
			assert.Equal(t, "child-perm", childRecorded.published[0].RequestID)
			if controlFirst {
				assert.Empty(t, childRecorded.published[0].NativeInputIDs)
				require.Equal(t, []agent.NativeControlInputs{{
					SessionID: "native-child", RequestID: "child-perm",
					InstanceID: childRecorded.published[0].InstanceID,
					InputIDs:   []string{gotSteerReqID},
				}}, childRecorded.NativeControlInputBindings())
			} else {
				assert.Equal(t, []string{gotSteerReqID}, childRecorded.published[0].NativeInputIDs)
				assert.Empty(t, childRecorded.NativeControlInputBindings())
			}

			require.Equal(t, []agent.SteerResolution{{
				InputID: "queue-child-steer", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeEchoed,
				Receipt: agent.NativeInputReceipt{SessionID: "native-child", InputID: gotSteerReqID, TurnID: "3"},
			}}, childBase.SteerResolutions())
			require.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "native-child", TurnID: "3",
				Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs:  []agent.NativeReplyBoundary{{InputID: gotSteerReqID, AfterOrdinal: 1}},
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
	var reqMu sync.Mutex
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
		reqMu.Lock()
		requestID = request.Payload.Args.Request.ID
		reqMu.Unlock()
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
	reqMu.Lock()
	gotRequestID := requestID
	reqMu.Unlock()
	require.NotEmpty(t, gotRequestID)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-root", InputID: gotRequestID}, receipt)

	applyReplyEvent(t, a, 1, "turn/start", `{"turn":12}`)
	applyReplyEvent(t, a, 2, "user/message", harnessInputEcho(gotRequestID))
	applyReplyEvent(t, a, 3, "assistant/message", `{"turn":12,"message":{"content":[{"type":"text","text":"Session reply."}]}}`)
	applyReplyEvent(t, a, 4, "turn/end", `{"turn":12,"reason":{"kind":"completed"}}`)
	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "native-root", TurnID: "12", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: gotRequestID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "session-event:3:block:0"}},
	}}, sink.NativeReplyTurns())
}

func TestStopRetiresUnfinishedRepliesAndRootAndChildSteers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "deepseek-stop-retire", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
	})
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: "native-child",
		AgentSessionID:   "native-child",
		Title:            "Child",
	})
	require.NoError(t, err)
	childSink := sink.Child(childID)

	a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "root-steer-req"}, &agent.SteerRequest{
		InputID: "root-steer-input", AttemptID: "root-attempt",
	})
	a.registerReplySubmission(a.sink.ChildSink(childID), agent.NativeInputReceipt{SessionID: "native-child", InputID: "child-steer-req"}, &agent.SteerRequest{
		InputID: "child-steer-input", AttemptID: "child-attempt",
	})
	a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "root-turn-input"}, nil)
	a.registerReplySubmission(nil, agent.NativeInputReceipt{SessionID: "native-root", InputID: "nil-sink-steer"}, &agent.SteerRequest{
		InputID: "nil-input", AttemptID: "nil-attempt",
	})
	a.Mu.Lock()
	a.pendingSteers = []*pendingSteer{{requestID: "root-steer-req", itemID: "item-root"}}
	a.Mu.Unlock()
	applyReplyEvent(t, a, 1, "turn/start", `{"turn":4}`)
	applyReplyEvent(t, a, 2, "user/message", harnessInputEcho("root-turn-input"))

	a.Stop()

	a.Mu.Lock()
	assert.Empty(t, a.pendingSteers, "Stop must clear pending root steers")
	a.Mu.Unlock()
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "root-steer-input", AttemptID: "root-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions(), "Stop must retire unechoed root steers as uncertain")
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "child-steer-input", AttemptID: "child-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, childSink.SteerResolutions(), "Stop must retire unechoed child steers as uncertain")
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		sink.NativeReplyRetirements()[agent.NativeInputReceipt{SessionID: "native-root", InputID: "root-turn-input", TurnID: "4"}],
		"Stop must retire active reply turns")

	// A subsequent Wait must not duplicate retirements.
	require.NoError(t, a.Wait())
	assert.Len(t, sink.SteerResolutions(), 1)
	assert.Len(t, childSink.SteerResolutions(), 1)
}

func TestReplyToolInputsSnapshotImmutabilityPruningAndUnboundCounter(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)

	a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "in-1"}, nil)
	applyReplyEvent(t, a, 1, "turn/start", `{"turn":10}`)
	applyReplyEvent(t, a, 2, "user/message", harnessInputEcho("in-1"))
	applyReplyEvent(t, a, 3, "tool/call", `{"turn":10,"name":"bash","callId":"call-1"}`)
	applyReplyEvent(t, a, 4, "tool/call", `{"turn":10,"name":"read","callId":"call-2"}`)

	a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "in-2"}, nil)
	applyReplyEvent(t, a, 5, "user/message", harnessInputEcho("in-2"))
	applyReplyEvent(t, a, 6, "tool/call", `{"turn":10,"name":"edit","callId":"call-3"}`)

	a.dispatchMu.Lock()
	assert.Equal(t, []string{"in-1"}, a.replyToolInputs("native-root", "call-1"))
	assert.Equal(t, []string{"in-1"}, a.replyToolInputs("native-root", "call-2"))
	assert.Equal(t, []string{"in-1", "in-2"}, a.replyToolInputs("native-root", "call-3"))
	a.dispatchMu.Unlock()

	// tool/result must prune toolInputs for that callID without touching sibling calls.
	applyReplyEvent(t, a, 7, "tool/result", `{"turn":10,"message":{"role":"tool","toolCallId":"call-1","content":[],"isError":false}}`)
	a.dispatchMu.Lock()
	assert.Nil(t, a.replyToolInputs("native-root", "call-1"))
	assert.Equal(t, []string{"in-1"}, a.replyToolInputs("native-root", "call-2"))
	assert.Equal(t, []string{"in-1", "in-2"}, a.replyToolInputs("native-root", "call-3"))
	a.dispatchMu.Unlock()

	// Publish an unbound control before its tool/call, then bind and cancel to check counter returns to zero.
	assert.Zero(t, a.unboundControls.Load())
	raw := fmt.Sprintf(
		`{"type":"item","streamId":"events","value":{"type":"waterfall","event":%q,"eventId":"ctrl-unbound","agentId":"native-root","request":{"toolName":"bash","callId":"call-future"}}}`,
		contracts.DeepseekHarnessControlEventApproval,
	)
	require.NoError(t, a.handleFrame([]byte(raw)))
	assert.Equal(t, int32(1), a.unboundControls.Load())

	applyReplyEvent(t, a, 8, "tool/call", `{"turn":10,"name":"bash","callId":"call-future"}`)
	assert.Zero(t, a.unboundControls.Load())
}

func TestUserQuestionsWaitCallIDBindsReplyControlOwnership(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	sink := newHarnessRecordedChildControlSink(base)
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)

	a.registerReplySubmission(a.sink, agent.NativeInputReceipt{SessionID: "native-root", InputID: "in-q"}, nil)
	applyReplyEvent(t, a, 1, "turn/start", `{"turn":11}`)
	applyReplyEvent(t, a, 2, "user/message", harnessInputEcho("in-q"))

	raw := fmt.Sprintf(
		`{"type":"item","streamId":"events","value":{"type":"waterfall","event":%q,"eventId":"q-wait","agentId":"native-root","request":{"wait":{"callId":"wait-call-1"},"questions":[{"id":"q1","question":"Continue?","options":[{"label":"Yes"}]}]}}}`,
		contracts.DeepseekHarnessControlEventUserQuestions,
	)
	require.NoError(t, a.handleFrame([]byte(raw)))
	assert.Equal(t, int32(1), a.unboundControls.Load())

	applyReplyEvent(t, a, 3, "tool/call", `{"turn":11,"name":"ask_user_question","callId":"wait-call-1"}`)
	assert.Zero(t, a.unboundControls.Load())
	sink.mu.Lock()
	require.Len(t, sink.published, 1)
	instanceID := sink.published[0].InstanceID
	sink.mu.Unlock()
	assert.Equal(t, []agent.NativeControlInputs{{
		SessionID: "native-root", RequestID: "q-wait", InstanceID: instanceID, InputIDs: []string{"in-q"},
	}}, sink.NativeControlInputBindings())
}
