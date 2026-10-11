package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiAbortRejectsAnAcknowledgementForAnotherCommand(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"get_state", ""} {
		t.Run(map[bool]string{false: "wrong command", true: "missing command"}[command == ""], func(t *testing.T) {
			t.Parallel()
			a, sink, _ := piQuestionResponseFixture()
			a.Mu.Lock()
			a.currentTurnActive = true
			a.Mu.Unlock()
			setPiInterruptWriter(a, func(data []byte) (int, error) {
				var request struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(data, &request); err != nil {
					return 0, err
				}
				reply := map[string]any{"type": "response", "id": request.ID, "success": true}
				if command != "" {
					reply["command"] = command
				}
				raw, err := json.Marshal(reply)
				if err != nil {
					return 0, err
				}
				a.HandleOutput(raw)
				return len(data), nil
			})
			require.Error(t, a.Interrupt(agent.StopContext{}), "a matching ID is not an abort acknowledgement for another command")
			assert.Empty(t, sink.CanceledControls())
			a.Mu.Lock()
			assert.True(t, a.currentTurnActive)
			assert.Contains(t, a.openDialogs, "select")
			assert.Contains(t, a.questionDialogs, "select")
			a.Mu.Unlock()
			output := &bytes.Buffer{}
			a.SetStdinForTest(agenttest.NopStdin(output))
			require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"select","value":"My retained answer"}`), agent.StopContext{}))
			a.HandleOutput([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
			frames := strings.Split(strings.TrimSpace(output.String()), "\n")
			require.Len(t, frames, 2)
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My retained answer"}`, frames[1])
			assert.Len(t, sink.PublishedControls(), 1)
		})
	}
}

func TestPiLaterInvalidAcknowledgementDoesNotRestoreACancelledQuestion(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"get_state", ""} {
		t.Run(map[bool]string{false: "wrong command", true: "missing command"}[command == ""], func(t *testing.T) {
			t.Parallel()
			a, sink, _ := piQuestionResponseFixture()
			clock := testutil.NewQuartzMock(t)
			a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "late-ack", Ctx: context.Background(), Clock: clock})
			a.Mu.Lock()
			a.currentTurnActive = true
			a.Mu.Unlock()
			var requestID string
			setPiInterruptWriter(a, func(data []byte) (int, error) {
				var request struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				}
				if err := json.Unmarshal(data, &request); err != nil {
					return 0, err
				}
				if request.Type == CommandAbort {
					requestID = request.ID
				}
				return len(data), nil
			})
			trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandAbort)
			t.Cleanup(trap.Close)
			result := make(chan error, 1)
			go func() { result <- a.Interrupt(agent.StopContext{}) }()
			call, err := trap.Wait(testutil.DeadlineContext(t))
			require.NoError(t, err, "the acknowledgement wait follows the completed cancellation sweep")
			assert.Equal(t, []string{"select"}, sink.CanceledControls())
			reply := map[string]any{"type": "response", "id": requestID, "success": true}
			if command != "" {
				reply["command"] = command
			}
			raw, err := json.Marshal(reply)
			require.NoError(t, err)
			a.HandleOutput(raw)
			call.MustRelease(testutil.DeadlineContext(t))
			trap.Close()
			require.Error(t, awaitPiInterrupt(t, result))
			a.Mu.Lock()
			assert.Empty(t, a.openDialogs)
			assert.Empty(t, a.questionDialogs)
			assert.Empty(t, a.customQuestionAnswers)
			a.Mu.Unlock()
			assert.Equal(t, []string{"select"}, sink.CanceledControls())
		})
	}
}
