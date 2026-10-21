package opencode

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeRetryAttempt(t *testing.T) {
	t.Parallel()
	// The fixture trims the E2E wire of a mid-stream cut and normalizes its
	// IDs. The daemon reports the failed attempt's part and its retry status
	// before it forwards the partial chunk, and its stored message keeps the
	// failed part's text, so only the stored read of the parts that the retry
	// did not supersede answers.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "stored parts discard the failed attempt", FramesPath: "testdata/retry_attempt.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a, _, peer := newDaemonGenerationAgent(t)
			a.SetSinkForTest(agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
			peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"failed","type":"text","text":"partial "},{"id":"kept","type":"text","text":"ANSWER-AFTER-RETRY"}]}`, http.StatusOK)
			return func(frame []byte) {
				var envelope struct {
					Method string          `json:"method"`
					Result json.RawMessage `json:"result"`
				}
				require.NoError(t, json.Unmarshal(frame, &envelope))
				switch {
				case envelope.Method != "":
					a.HandleOutput(frame)
				case envelope.Result != nil:
					a.FinishPromptRequestForTest("test-session", envelope.Result, nil)
				default:
					a.handleDaemonEvent(frame)
				}
			}
		},
		Want:      []agenttest.RetryAttemptText{{Text: "ANSWER-AFTER-RETRY", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"partial "},
	})
}
