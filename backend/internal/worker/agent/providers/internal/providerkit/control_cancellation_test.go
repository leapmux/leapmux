package providerkit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlCancellationWaitsForAnUnsettledApprovalFailure(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: output})}
	sink := &agenttest.ControlSink{}
	base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"approve"}`), map[string]any{"cancelled": true}, make(chan int))
	// Hold the existing completion executor. The approval has claimed the
	// response, but its unsupported value prevents any frame from entering stdin.
	base.approvalQueue = make(chan *controlApprovalBatch, 1)
	require.Equal(t, 1, base.ApproveOutstandingControlRequests(sink, nil))
	approval := <-base.approvalQueue
	batch, ready, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	assert.Nil(t, batch, "an unconfirmed approval must not be skipped before cancellation")
	assert.NotNil(t, ready)
	if batch != nil {
		require.NoError(t, batch.Settle(sink))
	}
	base.completeControlApprovals(approval)
	if ready != nil {
		select {
		case <-ready:
		default:
			t.Fatal("settlement did not release the cancellation waiter")
		}
	}
	assert.True(t, base.OutstandingControlForTest("jsonrpc:1"), "the failed approval leaves the request answerable")
}

func TestControlCancellationKeepsOneFramedWriteAndTheRequiredOrder(t *testing.T) {
	t.Parallel()
	for _, cancelFirst := range []bool{false, true} {
		for _, lengthFraming := range []bool{false, true} {
			t.Run(fmt.Sprintf("cancel-first=%t/length-framing=%t", cancelFirst, lengthFraming), func(t *testing.T) {
				t.Parallel()
				writer := &blockingWriter{release: make(chan struct{})}
				close(writer.release)
				base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: agenttest.NopStdin(writer)})}
				if lengthFraming {
					base.FrameMessage = func(data []byte) []byte { return fmt.Appendf(nil, "Content-Length: %d\r\n\r\n%s", len(data), data) }
				}
				sink := &agenttest.ControlSink{}
				for _, id := range []int{1, 2} {
					base.PublishControlRequestInSession(sink, "owned", fmt.Appendf(nil, `{"id":%d,"method":"permission"}`, id), map[string]any{"cancelled": true})
				}
				base.PublishControlRequestInSession(sink, "owned", []byte(`{"id":3,"method":"answerless"}`), nil)
				base.PublishControlRequestInSession(sink, "other", []byte(`{"id":4,"method":"foreign"}`), map[string]any{"cancelled": true})
				batch, ready, err := base.QueueControlCancellation("owned", "session/cancel", nil, cancelFirst, true)
				require.NoError(t, err)
				require.Nil(t, ready)
				require.NotNil(t, batch)
				require.NoError(t, batch.Settle(sink))
				require.NoError(t, batch.Settle(sink), "duplicate settlement must not read another receipt")
				frames := writer.Frames()
				require.Len(t, frames, 1, "responses and cancellation occupy one stdin queue entry")
				cancel, first, last := strings.Index(frames[0], `"method":"session/cancel"`), strings.Index(frames[0], `"id":1`), strings.Index(frames[0], `"id":2`)
				require.NotEqual(t, -1, cancel)
				require.NotEqual(t, -1, first)
				require.NotEqual(t, -1, last)
				if cancelFirst {
					assert.Less(t, cancel, first)
				} else {
					assert.Greater(t, cancel, last)
				}
				if lengthFraming {
					assert.Equal(t, 3, strings.Count(frames[0], "Content-Length:"))
				} else {
					assert.Len(t, strings.Split(frames[0], "\n"), 3)
				}
				assert.True(t, base.OutstandingControlForTest("jsonrpc:3"))
				assert.True(t, base.OutstandingControlForTest("jsonrpc:4"))
				assert.ElementsMatch(t, []string{"jsonrpc:1", "jsonrpc:2"}, sink.CanceledControls())
			})
		}
	}
}

func TestControlCancellationSettlesOnlyItsSurvivingInstances(t *testing.T) {
	t.Parallel()
	for _, written := range []int{0, 1} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("written=%d/replace=%t", written, replace), func(t *testing.T) {
				t.Parallel()
				writer := newGatedStdin(written, assert.AnError)
				unblock := sync.OnceFunc(func() { close(writer.release) })
				t.Cleanup(unblock)
				base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: writer})}
				sink := &agenttest.ControlSink{}
				original := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, nil)
				batch, ready, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
				require.NoError(t, err)
				require.Nil(t, ready)
				require.NotNil(t, batch)
				select {
				case <-writer.entered:
				case <-testutil.DeadlineContext(t).Done():
					t.Fatal("the cancellation did not enter stdin")
				}
				var replacement PublishedControlRequest
				if replace {
					require.True(t, base.WithdrawPublishedControlRequest(sink, original))
					replacement = base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"replacement"}`), map[string]any{"cancelled": true}, nil)
					require.NotEmpty(t, replacement.RequestID)
					sink.ResetCanceledControls()
				}
				unblock()
				err = batch.Settle(sink)
				assert.ErrorIs(t, err, assert.AnError)
				assert.Equal(t, written > 0, errors.Is(err, agent.ErrDeliveryUncertain))
				if replace {
					assert.True(t, base.ControlPublicationIsCurrent(replacement))
					assert.Empty(t, sink.CanceledControls())
				} else if written == 0 {
					assert.True(t, base.ControlPublicationIsCurrent(original))
					assert.Empty(t, sink.CanceledControls())
				} else {
					assert.False(t, base.ControlPublicationIsCurrent(original))
					assert.Equal(t, []string{"jsonrpc:1"}, sink.CanceledControls())
				}
			})
		}
	}
}

func TestControlCancellationWaitsForAnApprovalWriteFailure(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: agenttest.FailingStdin{}})}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, map[string]any{"approved": true})
	base.approvalQueue = make(chan *controlApprovalBatch, 1)
	require.Equal(t, 1, base.ApproveOutstandingControlRequests(sink, nil))
	approval := <-base.approvalQueue
	batch, ready, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	require.Nil(t, batch)
	require.NotNil(t, ready)
	base.completeControlApprovals(approval)
	select {
	case <-ready:
	default:
		t.Fatal("the failed approval did not settle")
	}
	assert.True(t, base.ControlPublicationIsCurrent(publication))
	batch, ready, err = base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	require.Nil(t, ready)
	require.NotNil(t, batch)
	assert.ErrorIs(t, batch.Settle(sink), assert.AnError)
	assert.True(t, base.ControlPublicationIsCurrent(publication))
}

func TestControlCancellationWaitsForAnEarlierCancellation(t *testing.T) {
	t.Parallel()
	writer := newGatedStdin(0, assert.AnError)
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: writer})}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, nil)
	first, _, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	require.NotNil(t, first)
	select {
	case <-writer.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the first cancellation did not enter stdin")
	}
	second, ready, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	assert.Nil(t, second, "a newer conversation must await ownership of the pending response")
	assert.NotNil(t, ready)
	unblock()
	assert.ErrorIs(t, first.Settle(sink), assert.AnError)
	if second != nil {
		assert.ErrorIs(t, second.Settle(sink), assert.AnError)
	}
	if ready != nil {
		select {
		case <-ready:
		default:
			t.Fatal("the first cancellation did not release response ownership")
		}
	}
	assert.True(t, base.ControlPublicationIsCurrent(publication))
}

func TestControlCancellationWaitsForAnUnsettledUserResponse(t *testing.T) {
	t.Parallel()
	writer := newGatedStdin(0, assert.AnError)
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: writer})}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, nil)
	userDone := make(chan error, 1)
	go func() {
		userDone <- base.SendRawInputToOutstandingRequest([]byte(`{"id":1,"result":{"choice":"user"}}`), agent.StopContext{})
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the user response did not enter stdin")
	}
	batch, ready, err := base.QueueControlCancellation("", "session/cancel", nil, false, false)
	require.NoError(t, err)
	assert.Nil(t, batch, "cancellation must not overtake an unresolved user answer")
	assert.NotNil(t, ready)
	unblock()
	select {
	case err := <-userDone:
		assert.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("the user response did not settle")
	}
	if batch != nil {
		assert.ErrorIs(t, batch.Settle(sink), assert.AnError)
	}
	if ready != nil {
		select {
		case <-ready:
		default:
			t.Fatal("the user response did not release its ownership")
		}
	}
	assert.True(t, base.ControlPublicationIsCurrent(publication))
}

func TestFailedUserResponseKeepsANativeWithdrawal(t *testing.T) {
	t.Parallel()
	writer := newGatedStdin(0, assert.AnError)
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{Stdin: writer})}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"permission"}`), map[string]any{"cancelled": true}, nil)
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
	assert.False(t, base.WithdrawOutstandingControlRequest(sink, "jsonrpc:1"), "a native echo cannot cancel an unconfirmed user decision yet")
	unblock()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("the failed user response did not settle")
	}
	assert.False(t, base.ControlPublicationIsCurrent(publication), "the native withdrawal must prevent restoration")
	assert.Equal(t, []string{"jsonrpc:1"}, sink.CanceledControls())
}
