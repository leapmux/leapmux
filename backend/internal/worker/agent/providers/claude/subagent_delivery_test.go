package claude

import (
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type claudeRetainedRestartSink struct {
	agent.ProviderServices
	deliveries int
}

func (sink *claudeRetainedRestartSink) PersistChildUserMessage(childAgentID, text string) error {
	sink.deliveries++
	return sink.ProviderServices.PersistChildUserMessage(childAgentID, text)
}

func TestClaudeRestartDoesNotResubmitARetainedDeliveredMessage(t *testing.T) {
	t.Parallel()
	for _, registryRefusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained message", true: "retained message and refused revival"}[registryRefusal], func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			services := &claudeRetainedRestartSink{ProviderServices: agent.NewProviderServices(sink)}
			a := newTestAgent(services)
			child := spawnAndFinishSubagent(t, a, sink)
			queue := child.OutputQueue()
			child.PersistErr = errors.New("the child storage is unavailable")
			if registryRefusal {
				sink.ReviveErr = errors.New("the registry is unavailable")
			}
			sendMessageTo(a, "delivery-1", "task-1")
			restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
			require.True(t, queue.HasPending(), "the real child queue must own the delivered message")
			restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
			assert.Equal(t, 1, services.deliveries, "a repeated native start must not resubmit a queue-owned message")

			// The recording sink keeps failed attempts. Only writes after storage
			// recovers can establish how many delivered messages committed.
			beforeRecovery := len(child.Messages())
			child.PersistErr = nil
			require.NoError(t, queue.Flush())
			recovered := child.Messages()[beforeRecovery:]
			require.Len(t, recovered, 1, "the retained delivery must commit exactly once")
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, recovered[0].Source)
			assert.JSONEq(t, `{"content":"Check the remaining tests."}`, string(recovered[0].Content))
			assert.False(t, queue.HasPending())
			sink.ReviveErr = nil
			restartTaskStarted(a, "delivery-1", "Check the remaining tests.")
			assert.Equal(t, 1, services.deliveries, "a registry retry must preserve the retained-delivery fact")
			_, status, found, err := sink.LookupBackgroundTask("task-1")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, bgtask.StatusRunning, status)
		})
	}
}

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
