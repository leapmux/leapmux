package kimi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiTaskOutputReaderAnswersNativeHeartbeats(t *testing.T) {
	t.Parallel()
	for _, afterAcknowledgement := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_acknowledgement=%t", afterAcknowledgement), func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			pongs := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				connection, err := websocket.Accept(w, request, nil)
				if !assert.NoError(t, err) {
					return
				}
				defer connection.CloseNow() //nolint:errcheck // The client can finish first.
				_, raw, err := connection.Read(ctx)
				if !assert.NoError(t, err) {
					return
				}
				var command fakeKimiTaskCommand
				if !assert.NoError(t, json.Unmarshal(raw, &command)) {
					return
				}
				assert.Equal(t, kimiFrameTaskWatch, command.Type)
				assert.Equal(t, "session_1", command.SessionID)
				assert.Equal(t, "agent-owner", command.AgentID)
				assert.Equal(t, "task-output", command.TaskID)
				write := func(value any) bool {
					data, err := json.Marshal(value)
					return assert.NoError(t, err) && assert.NoError(t, connection.Write(ctx, websocket.MessageText, data))
				}
				response := map[string]any{"type": "response", "request_id": command.RequestID, "code": 0,
					"task": map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false, "child_agent_id": "agent-0"}}
				if afterAcknowledgement && !write(response) {
					return
				}
				if !write(map[string]any{"type": "ping"}) {
					return
				}
				_, pong, err := connection.Read(ctx)
				if !assert.NoError(t, err) {
					return
				}
				pongs <- pong
				if !afterAcknowledgement && !write(response) {
					return
				}
				write(map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 7, "text": "Native result 🙂"})
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			stream := newKimiStream(endpoint, "test-agent", quartz.NewReal(), nil, nil)
			output, err := stream.readTaskOutput(ctx, "session_1", kimiTaskAddress{ownerAgentID: "agent-owner", taskID: "task-output"})
			require.NoError(t, err)
			assert.EqualValues(t, 7, output.offset)
			assert.Equal(t, "Native result 🙂", output.text)
			select {
			case pong := <-pongs:
				assert.JSONEq(t, `{"type":"pong"}`, string(pong))
			case <-ctx.Done():
				t.Fatal("the task-output connection did not answer its native heartbeat")
			}
		})
	}
}

func TestKimiTaskOutputReaderUsesTheCompleteNativeTarget(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "native 🙂 output"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				assert.Equal(t, "agent-owner", command.AgentID)
				require.NotNil(t, command.TailBytes)
				assert.Equal(t, 1<<20, *command.TailBytes)
				return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false}
			}
			rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
				return []any{
					map[string]any{"type": "task.output", "session_id": "foreign", "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": "wrong session"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": "foreign", "task_id": command.TaskID, "offset": 0, "text": "wrong owner"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": "foreign", "offset": 0, "text": "wrong task"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": text},
				}
			}
			rig.fake.mu.Unlock()
			output, err := rig.agent.stream.readTaskOutput(testutil.DeadlineContext(t), "session_1", kimiTaskAddress{ownerAgentID: "agent-owner", taskID: "task-output"})
			require.NoError(t, err)
			assert.Zero(t, output.offset)
			assert.Equal(t, text, output.text)
		})
	}
}

func TestKimiTaskOutputReaderRejectsInvalidOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		offset any
		text   any
	}{
		{"missing offset", nil, "text"},
		{"negative offset", -1, "text"},
		{"missing text", 0, nil},
		{"oversized text", 0, strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false}
			}
			rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
				return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": test.offset, "text": test.text}}
			}
			rig.fake.mu.Unlock()
			_, err := rig.agent.stream.readTaskOutput(testutil.DeadlineContext(t), "session_1", kimiTaskAddress{ownerAgentID: "main", taskID: "task-output"})
			require.Error(t, err)
		})
	}
}
