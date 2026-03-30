package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func newCursorAgentWithSink(sink OutputSink) *CursorCLIAgent {
	a := &CursorCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID: "test-agent",
			}},
			sink:         sink,
			providerName: "cursor",
			sessionID:    "test-session",
		},
	}
	a.extraSessionUpdate = a.handleExtraSessionUpdate
	a.extraMethod = a.handleExtraMethod
	return a
}

func TestHandleCursorOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	if agent.permissionMode != CursorCLIModePlan {
		t.Fatalf("expected mode plan, got %q", agent.permissionMode)
	}
	if agent.model != "auto" {
		t.Fatalf("expected model auto, got %q", agent.model)
	}
	if got := sink.PermissionMode(); got != CursorCLIModePlan {
		t.Fatalf("expected sink permission mode plan, got %q", got)
	}
	if len(agent.availableModels) != 2 {
		t.Fatalf("expected 2 available models, got %d", len(agent.availableModels))
	}
	if agent.availableModels[0].GetId() != "auto" {
		t.Fatalf("expected normalized auto model, got %q", agent.availableModels[0].GetId())
	}
}

func TestHandleCursorOutput_AskQuestionPersistsControlRequest(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"toolCallId":"tc-1","title":"Need input","questions":[{"id":"q1","prompt":"Pick one","allowMultiple":false,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Beta"}]}]}}`
	agent.HandleOutput([]byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}
	if got := sink.LastPersistedControl().RequestID; got != "7" {
		t.Fatalf("expected control request id 7, got %q", got)
	}

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
	if err := json.Unmarshal(sink.LastPersistedControl().Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal control payload: %v", err)
	}
	if payload.Method != cursorMethodAskQuestion {
		t.Fatalf("expected cursor ask method, got %q", payload.Method)
	}
	if payload.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("expected AskUserQuestion wrapper, got %q", payload.Request.ToolName)
	}
	if len(payload.Request.Input.Questions) != 1 || payload.Request.Input.Questions[0].ID != "q1" {
		t.Fatalf("expected wrapped cursor question, got %#v", payload.Request.Input.Questions)
	}
}

func TestHandleCursorOutput_CreatePlanPersistsControlRequest(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCursorAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":8,"method":"cursor/create_plan","params":{"toolCallId":"plan-1","name":"Migration","overview":"Review the generated plan"}}`
	agent.HandleOutput([]byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}

	var payload struct {
		Type   string `json:"type"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(sink.LastPersistedControl().Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal create-plan payload: %v", err)
	}
	if payload.Type != "cursor.create_plan" || payload.Method != cursorMethodCreatePlan {
		t.Fatalf("expected cursor create-plan payload, got %#v", payload)
	}
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
				agentID:     "test-agent",
				stdin:       writePipe,
				ctx:         ctx,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			}},
			sink:         &testSink{},
			providerName: "cursor",
			sessionID:    "test-session",
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
	if got := int(resp["id"].(float64)); got != 9 {
		t.Fatalf("expected id 9, got %d", got)
	}
	if _, ok := resp["result"]; !ok {
		t.Fatalf("expected result response, got %#v", resp)
	}
}
