package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishedControlWithdrawalKeepsAReusedRequestID(t *testing.T) {
	done := make(chan struct{})
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Ctx: t.Context(), Stdin: agenttest.NopStdin(&agenttest.Stdin{}), ProcessDone: done})}
	t.Cleanup(func() { close(done); base.Stop() })
	sink := &agenttest.ControlSink{}
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"request"}`)
	first := base.PublishControlRequestWithApproval(sink, raw, nil, nil)
	assert.True(t, base.ControlPublicationIsCurrent(first))
	require.NoError(t, base.SendRawInput([]byte(`{"id":7,"result":{}}`), agent.StopContext{}))
	second := base.PublishControlRequestWithApproval(sink, raw, nil, nil)
	assert.False(t, base.ControlPublicationIsCurrent(first))
	assert.True(t, base.ControlPublicationIsCurrent(second))
	assert.False(t, base.WithdrawPublishedControlRequest(sink, first))
	assert.False(t, base.WithdrawPublishedControlRequest(sink, PublishedControlRequest{RequestID: second.RequestID}))
	assert.False(t, base.ControlPublicationIsCurrent(PublishedControlRequest{RequestID: second.RequestID}))
	assert.True(t, base.WithdrawPublishedControlRequest(sink, second))
	assert.False(t, base.WithdrawPublishedControlRequest(sink, second))
	assert.False(t, base.ControlPublicationIsCurrent(second))
	assert.Equal(t, []string{second.RequestID}, sink.CanceledControls())
}

func TestPublishedControlWithdrawalRunsOnceAcrossCallers(t *testing.T) {
	base := &JSONRPCProcess{}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":7,"method":"request"}`), nil, nil)
	start := make(chan struct{})
	results := make(chan bool, 2)
	for range 2 {
		go func() { <-start; results <- base.WithdrawPublishedControlRequest(sink, publication) }()
	}
	close(start)
	ctx := testutil.DeadlineContext(t)
	withdrawn := 0
	for range 2 {
		select {
		case result := <-results:
			if result {
				withdrawn++
			}
		case <-ctx.Done():
			t.Fatal("the control withdrawal did not finish")
		}
	}
	assert.Equal(t, 1, withdrawn)
	assert.Equal(t, []string{publication.RequestID}, sink.CanceledControls())
}
