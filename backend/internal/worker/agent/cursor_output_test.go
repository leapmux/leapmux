package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func newCursorAgentWithSink(sink OutputSink) *CursorCLIAgent {
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
	a.extraSessionUpdate = configOptionSessionUpdateHandler(a.handleConfigOptionUpdate)
	a.extraMethod = a.handleExtraMethod
	return a
}

func TestHandleCursorOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, CursorCLIModePlan, agent.permissionMode)
	require.Equal(t, "auto", agent.model)
	require.Equal(t, CursorCLIModePlan, sink.PermissionMode())
	require.Len(t, agent.availableModels, 2)
	require.Equal(t, "auto", agent.availableModels[0].GetId())
}

func TestHandleCursorOutput_AskQuestionPersistsControlRequest(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"toolCallId":"tc-1","title":"Need input","questions":[{"id":"q1","prompt":"Pick one","allowMultiple":false,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Beta"}]}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, "7", sink.LastPersistedControl().RequestID)

	var payload struct {
		Method  string `json:"method"`
		Request struct {
			ToolName string `json:"tool_name"`
			Input    struct {
				Questions []struct {
					ID          string `json:"id"`
					Question    string `json:"question"`
					Header      string `json:"header"`
					MultiSelect bool   `json:"multiSelect"`
				} `json:"questions"`
			} `json:"input"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(sink.LastPersistedControl().Payload, &payload))
	require.Equal(t, CursorMethodAskQuestion, payload.Method)
	require.Equal(t, "AskUserQuestion", payload.Request.ToolName)
	require.Len(t, payload.Request.Input.Questions, 1)
	require.Equal(t, "q1", payload.Request.Input.Questions[0].ID)
}

func TestHandleCursorOutput_CreatePlanPersistsControlRequest(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":8,"method":"cursor/create_plan","params":{"toolCallId":"plan-1","name":"Migration","overview":"Review the generated plan"}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())

	var payload struct {
		Type   string `json:"type"`
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal(sink.LastPersistedControl().Payload, &payload))
	require.Equal(t, "cursor.create_plan", payload.Type)
	require.Equal(t, CursorMethodCreatePlan, payload.Method)
}

func TestHandleCursorOutput_UpdateTodosAcknowledgesRequest(t *testing.T) {
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
