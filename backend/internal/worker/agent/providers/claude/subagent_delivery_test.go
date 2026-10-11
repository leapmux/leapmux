package claude

import (
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeRestartRetryKeepsOneDeliveredMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{ReviveErr: errors.New("the registry write failed")}
	a := newTestAgent(agent.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	sendMessageTo(a, "delivery-1", "task-1")
	restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
	require.Len(t, child.Messages(), before+1)
	sink.ReviveErr = nil
	restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
	require.Len(t, child.Messages(), before+1, "a registry retry must not repeat a delivered message")
	assert.JSONEq(t, `{"content":"Check the remaining tests."}`, string(child.Messages()[before].Content))
	_, status, found, err := sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, status)
}

type claudeRestartDeliveryFailureSink struct {
	agent.ProviderServices
	fail bool
}

func (sink *claudeRestartDeliveryFailureSink) PersistChildUserMessage(childAgentID, text string) error {
	if sink.fail {
		return errors.New("the child message write failed")
	}
	return sink.ProviderServices.PersistChildUserMessage(childAgentID, text)
}

func TestClaudeRestartRetryPreservesAFailedDeliveredMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := &claudeRestartDeliveryFailureSink{ProviderServices: agent.NewProviderServices(sink)}
	a := newTestAgent(services)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	services.fail = true
	sendMessageTo(a, "delivery-1", "task-1")
	restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
	assert.Len(t, child.Messages(), before)
	_, status, found, err := sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, status.IsFinished(), "the failed delivery must remain eligible for retry")
	services.fail = false
	restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
	require.Len(t, child.Messages(), before+1, "a later start must retry the failed delivered message")
	assert.JSONEq(t, `{"content":"Check the remaining tests."}`, string(child.Messages()[before].Content))
	_, status, found, err = sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, status)
}
