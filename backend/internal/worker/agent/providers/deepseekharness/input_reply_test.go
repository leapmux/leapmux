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
				receipt, err := a.SendInputWithReceipt("Check the result.", nil)
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
			a.registerReplySubmission(receipt)
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
			a.registerReplySubmission(agent.NativeInputReceipt{SessionID: "native-root", InputID: "input"})
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
			a.registerReplySubmission(agent.NativeInputReceipt{SessionID: "native-root", InputID: "later-input"})
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
