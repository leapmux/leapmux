package agent

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.CursorMethodAskQuestion, contracts.CursorMethodCreatePlan} {
		t.Run(method, func(t *testing.T) {
			output := &syncBuffer{}
			sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
			a := newCursorAgentWithSink(sink)
			a.stdin = nopWriteCloser{output}
			a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"007","method":"` + method + `","params":{}}`))
			// HandleOutput runs on the goroutine that drains Cursor's stdout, so the
			// failure reply is QUEUED rather than written before it returns.
			var answer string
			require.Eventually(t, func() bool {
				answer = output.String()
				return answer != ""
			}, 2*time.Second, 5*time.Millisecond, "the publication failure is answered")
			require.JSONEq(t, `{"jsonrpc":"2.0","id":"007","error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, answer)
			require.Empty(t, sink.PublishedControls())
		})
	}
}

func newCursorAgentWithSink(sink ProviderServices) *CursorCLIAgent {
	a := &CursorCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "cursor",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
	a.modelIDNormalizer = normalizeCursorModelID
	a.modeChannel = modeChannelPermissionMode
	a.extraMethod = a.handleExtraMethod
	a.sink = newModelProgressResetSink(a.sink)
	return a
}

func TestHandleCursorOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCursorAgentWithSink(sink)
	agent.permissionMode = CursorCLIModeAgent

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, CursorCLIModePlan, agent.permissionMode)
	require.Equal(t, "auto", agent.model)
	require.Len(t, agent.availableModels, 2)
	require.Equal(t, "auto", agent.availableModels[0].GetId())
	// The combined model+mode update broadcasts the full settings once (StatusChange
	// carrying the new mode) plus the chat notification -- not a second
	// UpdatePermissionMode StatusChange.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	require.Equal(t, "auto", refresh.Model)
	require.Equal(t, CursorCLIModePlan, refresh.PermissionMode)
	require.Equal(t, []testSinkModeChange{{Old: CursorCLIModeAgent, New: CursorCLIModePlan}}, sink.ModeChanges())
	require.Empty(t, sink.PermissionMode(), "combined change must not also fire UpdatePermissionMode")
}

func TestHandleCursorOutput_AskQuestionPersistsControlRequest(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"toolCallId":"tc-1","title":"Need input","questions":[{"id":"q1","prompt":"Pick one","allowMultiple":false,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Beta"}]}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PublishedControlCount())
	require.Equal(t, "jsonrpc:7", sink.LastPublishedControl().RequestID)

	require.Equal(t, input, string(sink.LastPublishedControl().Payload))
}

func TestHandleCursorOutput_CreatePlanPersistsControlRequest(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	agent := newCursorAgentWithSink(sink)

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

	agent := &CursorCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "cursor",
				stdin:        writePipe,
				ctx:          ctx,
				cancel:       cancel,
				processDone:  make(chan struct{}),
				stderrDone:   make(chan struct{}),
			}},
			sink:      &testSink{},
			sessionID: "test-session",
		},
	}
	agent.extraMethod = agent.handleExtraMethod

	done := make(chan map[string]interface{}, 1)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		if scanner.Scan() {
			var payload map[string]interface{}
			_ = json.Unmarshal(scanner.Bytes(), &payload)
			done <- payload
		}
	}()

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":9,"method":"cursor/update_todos","params":{"toolCallId":"todo-1","todos":[]}}`))

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
			sink := &testSink{}
			written := &syncBuffer{}
			a := newCursorAgentWithSink(sink)
			a.ctx = context.Background()
			a.stdin = nopWriteCloser{written}

			a.HandleOutput([]byte(tc.raw))

			require.Eventually(t, func() bool {
				return len(sink.Messages()) == 1
			}, 2*time.Second, 5*time.Millisecond, "the frame must reach the transcript")
			assert.Equal(t, []byte(tc.raw), sink.Messages()[0].Content)

			if tc.wantReply {
				require.Eventually(t, func() bool {
					return strings.Contains(written.String(), `"code":-32601`)
				}, 2*time.Second, 5*time.Millisecond, "an unsupported request is still answered")
			} else {
				require.Never(t, func() bool { return written.String() != "" },
					200*time.Millisecond, 5*time.Millisecond, "a notification draws no reply")
			}
		})
	}
}
