package cursor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.CursorMethodAskQuestion, contracts.CursorMethodCreatePlan} {
		t.Run(method, func(t *testing.T) {
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
			a := newCursorAgentWithSink(agent.NewProviderServices(sink))
			a.SetStdinForTest(agenttest.NopStdin(output))
			a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"007","method":"` + method + `","params":{}}`))
			// HandleOutput runs on the goroutine that drains Cursor's stdout, so the
			// failure reply is QUEUED rather than written before it returns.
			var answer string
			require.Eventually(t, func() bool {
				answer = output.String()
				return answer != ""
			}, 30*time.Second, 5*time.Millisecond, "the publication failure is answered")
			require.JSONEq(t, `{"jsonrpc":"2.0","id":"007","error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, answer)
			require.Empty(t, sink.PublishedControls())
		})
	}
}

func newCursorAgentWithSink(sink agent.ProviderServices) *Agent {
	a := &Agent{
		Base: acp.Base{
			JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID:      "test-agent",
				ProviderName: "cursor",
			})},
		},
	}
	a.SetSinkForTest(sink)
	a.SetSessionIDForTest("test-session")
	a.HooksForTest().ModelIDNormalizer = normalizeCursorModelID
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.HooksForTest().ExtraMethod = a.handleExtraMethod
	a.SetSinkForTest(agent.NewModelProgressResetSink(a.Sink()))
	return a
}

func TestHandleCursorOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCursorAgentWithSink(agent.NewProviderServices(sink))
	agent.SetPermissionModeForTest(ModeAgent)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, ModePlan, agent.PermissionModeForTest())
	require.Equal(t, "auto", agent.ModelForTest())
	require.Len(t, agent.AvailableModelsForTest(), 2)
	require.Equal(t, "auto", agent.AvailableModelsForTest()[0].GetId())
	// The combined model+mode update broadcasts the full settings once (StatusChange
	// carrying the new mode) plus the chat notification -- not a second
	// UpdatePermissionMode StatusChange.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	require.Equal(t, "auto", refresh.Model)
	require.Equal(t, ModePlan, refresh.PermissionMode)
	require.Equal(t, []agenttest.ModeChange{{Old: ModeAgent, New: ModePlan}}, sink.ModeChanges())
	require.Empty(t, sink.PermissionMode(), "combined change must not also fire UpdatePermissionMode")
}

func TestHandleCursorOutput_AskQuestionPersistsControlRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCursorAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"toolCallId":"tc-1","title":"Need input","questions":[{"id":"q1","prompt":"Pick one","allowMultiple":false,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Beta"}]}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PublishedControlCount())
	require.Equal(t, "jsonrpc:7", sink.LastPublishedControl().RequestID)

	require.Equal(t, input, string(sink.LastPublishedControl().Payload))
}

func TestHandleCursorOutput_CreatePlanPersistsControlRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCursorAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":8,"method":"cursor/create_plan","params":{"toolCallId":"plan-1","name":"Migration","overview":"Review the generated plan"}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PublishedControlCount())

	require.Equal(t, "jsonrpc:8", sink.LastPublishedControl().RequestID)
	require.Equal(t, input, string(sink.LastPublishedControl().Payload))
}

func TestHandleCursorOutput_UpdateTodosAcknowledgesRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	}()

	a := &Agent{
		Base: acp.Base{
			JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID:      "test-agent",
				ProviderName: "cursor",
				Stdin:        writePipe,
				Ctx:          ctx,
				Cancel:       cancel,
				ProcessDone:  make(chan struct{}),
				StderrDone:   make(chan struct{}),
			})},
		},
	}
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetSessionIDForTest("test-session")
	a.HooksForTest().ExtraMethod = a.handleExtraMethod

	done := make(chan map[string]interface{}, 1)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		if scanner.Scan() {
			var payload map[string]interface{}
			_ = json.Unmarshal(scanner.Bytes(), &payload)
			done <- payload
		}
	}()

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":9,"method":"cursor/update_todos","params":{"toolCallId":"todo-1","todos":[]}}`))

	resp := <-done
	require.Equal(t, 9, int(resp["id"].(float64)))
	_, ok := resp["result"]
	require.True(t, ok)
}

// An unrecognized `cursor/` method must reach the transcript, like every other
// provider's unknown frame.
//
// Cursor answered -32601 from its own default and returned true, which
// short-circuited the shared ACP default that persists the frame. The runtime got a
// correct reply and the reader got no row at all, with no way to see what Cursor sent.
// The `cursor/` namespace is open and only five names are known, so a new one is the
// ordinary case.
func TestCursorUnknownExtensionMethodReachesTheTranscript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		raw       string
		wantReply bool
	}{
		{
			name:      "a request draws a refusal and a row",
			raw:       `{"jsonrpc":"2.0","id":7,"method":"cursor/something_new","params":{"value":1}}`,
			wantReply: true,
		},
		{
			name:      "a notification draws a row and no reply",
			raw:       `{"jsonrpc":"2.0","method":"cursor/something_happened","params":{"value":1}}`,
			wantReply: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			written := &agenttest.Stdin{}
			a := newCursorAgentWithSink(agent.NewProviderServices(sink))
			a.SetContextForTest(context.Background())
			a.SetStdinForTest(agenttest.NopStdin(written))

			a.HandleOutput([]byte(tc.raw))

			require.Eventually(t, func() bool {
				return len(sink.Messages()) == 1
			}, 30*time.Second, 5*time.Millisecond, "the frame must reach the transcript")
			assert.Equal(t, []byte(tc.raw), sink.Messages()[0].Content)

			if tc.wantReply {
				require.Eventually(t, func() bool {
					return strings.Contains(written.String(), `"code":-32601`)
				}, 30*time.Second, 5*time.Millisecond, "an unsupported request is still answered")
			} else {
				require.Never(t, func() bool { return written.String() != "" },
					200*time.Millisecond, 5*time.Millisecond, "a notification draws no reply")
			}
		})
	}
}

// A dialog of a session that the agent no longer serves -- the session that a
// context clear replaced -- never reaches the reader: its updates reach no
// transcript, so a card for it would let the reader approve work that LeapMux
// never shows. Cursor waits on the answer, so the dialog is refused at once: the
// plan with Cursor's own rejection, and the question, which has no cancel answer,
// with a JSON-RPC error.
func TestCursorRefusesADialogOfASessionThatItDoesNotServe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method string
		want   string
	}{
		{contracts.CursorMethodCreatePlan, `{"jsonrpc":"2.0","id":"007","result":{"outcome":{"outcome":"rejected"}}}`},
		{contracts.CursorMethodAskQuestion, ``},
	} {
		t.Run(tc.method, func(t *testing.T) {
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{}
			a := newCursorAgentWithSink(agent.NewProviderServices(sink))
			a.SetStdinForTest(agenttest.NopStdin(output))
			a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"007","method":"` + tc.method + `","params":{"sessionId":"old-session"}}`))

			var answer string
			require.Eventually(t, func() bool {
				answer = output.String()
				return answer != ""
			}, 30*time.Second, 5*time.Millisecond, "the dialog of the retired session is answered")
			if tc.want != "" {
				require.JSONEq(t, tc.want, answer)
			} else {
				var reply struct {
					ID     string          `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  *struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(answer)), &reply))
				assert.Equal(t, "007", reply.ID)
				require.NotNil(t, reply.Error, "a question has no cancel answer, so it takes an error")
				assert.NotZero(t, reply.Error.Code)
				assert.Contains(t, reply.Error.Message, "old-session", "the error states the session that the agent no longer serves")
				assert.Empty(t, reply.Result, "an error carries no decision")
			}
			assert.Empty(t, sink.PublishedControls(), "no card reaches the reader")
		})
	}
}

// The current session's dialogs still reach the reader, and each one waits for
// the reader's decision.
func TestCursorPublishesADialogOfItsOwnSession(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.CursorMethodAskQuestion, contracts.CursorMethodCreatePlan} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newCursorAgentWithSink(agent.NewProviderServices(sink))
			a.SetStdinForTest(agenttest.NopStdin(&agenttest.Stdin{}))
			raw := `{"jsonrpc":"2.0","id":"008","method":"` + method + `","params":{"sessionId":"test-session"}}`

			a.HandleOutput([]byte(raw))

			published := sink.PublishedControls()
			require.Len(t, published, 1)
			assert.JSONEq(t, raw, string(published[0].Payload), "the browser reads Cursor's own bytes")
			assert.Equal(t, 1, a.OutstandingControlCountForTest(), "the dialog waits for the reader")
		})
	}
}
