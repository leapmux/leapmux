package kilo

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiloRetryAttempt(t *testing.T) {
	t.Parallel()
	// Kilo ends the failed attempt without a retry in kilo/logs/r2-mockmid-retryable-1.log.
	// The fixture retains its partial output and native error, with normalized IDs.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "a failed stream ends without joining another attempt", FramesPath: "testdata/retry_attempt.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a := &Agent{}
			a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", ProviderName: "kilo", Ctx: t.Context(), Clock: testutil.NewQuartzMock(t)})
			a.SetSinkForTest(agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
			a.SetSessionIDForTest("test-session")
			a.SetPromptActiveForTest(true)
			*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
			return func(frame []byte) {
				var envelope struct {
					Method string                            `json:"method"`
					Error  *providerkit.JSONRPCResponseError `json:"error"`
				}
				require.NoError(t, json.Unmarshal(frame, &envelope))
				if envelope.Method != "" {
					a.HandleOutput(frame)
				} else {
					require.NotNil(t, envelope.Error)
					a.FinishPromptRequestForTest("test-session", nil, envelope.Error)
					assert.Empty(t, sink.PersistedNotifications())
				}
			}
		},
		Want: []agenttest.RetryAttemptText{{Text: "partial ", Completion: agent.MessageCompletionError}},
	})
}

func TestKiloClassifiesNativeRetry(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.APIRetryNotification(), Registration().Plugin.Classify(json.RawMessage(`{"type":"session.status","properties":{"status":{"type":"retry","attempt":2}}}`)))
	for _, raw := range []string{`{}`, `{"type":"session.status"}`, `{"type":"session.status","properties":{"status":{"type":"idle"}}}`, `null`, `{`} {
		assert.Equal(t, agent.NotificationClassification{}, Registration().Plugin.Classify(json.RawMessage(raw)))
	}
}
