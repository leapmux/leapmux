package providerkit

import (
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responseInstanceSink struct {
	agenttest.ControlSink
	mu        sync.Mutex
	cancelled []string
}

func (sink *responseInstanceSink) CancelControlRequestInstance(requestID, instanceID string) {
	sink.mu.Lock()
	sink.cancelled = append(sink.cancelled, instanceID)
	sink.mu.Unlock()
	sink.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func (sink *responseInstanceSink) instances() []string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]string(nil), sink.cancelled...)
}

func TestUserResponseSettlementKeepsNativeWithdrawalAndReplacementIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label   string
		written int
		err     error
	}{
		{label: "successful answer"},
		{label: "definite failure", err: assert.AnError},
		{label: "uncertain answer", written: 1, err: assert.AnError},
	} {
		for _, force := range []bool{false, true} {
			for _, replace := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/force=%t/replace=%t", tc.label, force, replace), func(t *testing.T) {
					t.Parallel()
					writer := newGatedStdin(tc.written, tc.err)
					unblock := sync.OnceFunc(func() { close(writer.release) })
					t.Cleanup(unblock)
					base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: writer})}
					sink := &responseInstanceSink{}
					original := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, nil)
					done := make(chan error, 1)
					go func() {
						done <- base.SendRawInputToOutstandingRequest([]byte(`{"id":1,"result":{"choice":"user"}}`), agent.StopContext{})
					}()
					ctx := testutil.DeadlineContext(t)
					select {
					case <-writer.entered:
					case <-ctx.Done():
						t.Fatal("the user response did not enter stdin")
					}
					if force {
						base.WithdrawControlRequest(sink, original.RequestID)
					} else {
						assert.False(t, base.WithdrawOutstandingControlRequest(sink, original.RequestID))
						assert.Empty(t, sink.instances(), "an unconfirmed native echo cannot erase the user's answer")
					}
					var replacement PublishedControlRequest
					if replace {
						replacement = base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"replacement"}`), nil, nil)
						require.NotEmpty(t, replacement.RequestID)
						require.NotEqual(t, original.record.instanceID, replacement.record.instanceID)
					}
					unblock()
					select {
					case err := <-done:
						if tc.err != nil {
							assert.ErrorIs(t, err, tc.err)
						} else {
							require.NoError(t, err)
						}
					case <-ctx.Done():
						t.Fatal("the user response did not settle")
					}
					assert.False(t, base.ControlPublicationIsCurrent(original))
					if replace {
						assert.True(t, base.ControlPublicationIsCurrent(replacement))
					}
					if force || tc.err != nil && tc.written == 0 {
						assert.Equal(t, []string{original.record.instanceID}, sink.instances())
					} else {
						assert.Empty(t, sink.instances())
					}
				})
			}
		}
	}
}

func TestExplicitControlAnswerCannotClaimAReplacementPublication(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: output})}
	sink := &agenttest.ControlSink{}
	original := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"original"}`), nil, nil)
	require.True(t, base.WithdrawPublishedControlRequest(sink, original))
	replacement := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"replacement"}`), nil, nil)
	assert.False(t, base.AnswerPublishedControlRequest(sink, original, map[string]any{"choice": "old"}, nil))
	assert.True(t, base.ControlPublicationIsCurrent(replacement))
	complete := make(chan error, 1)
	require.True(t, base.AnswerPublishedControlRequest(sink, replacement, map[string]any{"choice": "new"}, func(err error) { complete <- err }))
	select {
	case err := <-complete:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the explicit answer did not settle")
	}
	assert.NotContains(t, output.String(), `"choice":"old"`)
	assert.Contains(t, output.String(), `"choice":"new"`)
}
