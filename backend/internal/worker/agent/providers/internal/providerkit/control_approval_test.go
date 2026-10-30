package providerkit

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlApprovalHandleCannotClaimAReplacement(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)
	old := approvalHandle(base, "jsonrpc:1")
	base.WithdrawControlRequest(sink, old.RequestID)
	replacement := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"new"}`), nil, map[string]any{"decision": "accept"})
	require.NotEmpty(t, replacement.RequestID)
	assert.NotEqual(t, old.record.instanceID, replacement.record.instanceID)
	assert.False(t, base.ApproveOutstandingControlRequest(sink, old))
	assert.True(t, base.OutstandingControlForTest(replacement.RequestID))
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.NotContains(t, output.String(), `"id":1`)
}

func TestControlApprovalAdmissionFailureAnswersAFailedPublication(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)
	handle := approvalHandle(base, "jsonrpc:1")
	base.outstandingMu.Lock()
	handle.record.response = newControlResponseClaim(controlResponseApproval)
	handle.record.publicationErr = errors.New("publication failed while approval waited for admission")
	base.outstandingMu.Unlock()
	base.releaseControlApprovals([]controlResponseWrite{{requestID: handle.RequestID, record: handle.record, claim: handle.record.response}})
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.Contains(t, output.String(), `"error":{"code":-32603`)
	assert.False(t, base.OutstandingControlForTest(handle.RequestID))
	assert.Empty(t, sink.CanceledControls())
}

func TestUnpublishedApprovalAdmissionFailureNeverReopensTheRecord(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{}
	identity, ok := agent.NewControlRequestIdentity([]byte(`1`))
	require.True(t, ok)
	record := base.registerControl(identity, &outstandingControlRequest{response: newControlResponseClaim(controlResponseApproval), publishOnFailure: true})
	base.releaseControlApprovals([]controlResponseWrite{{requestID: identity.Key, record: record, claim: record.response}})
	assert.False(t, base.OutstandingControlForTest(identity.Key), "the caller republishes after refusal; no sweep can claim an intermediate open record")
}

func TestUnpublishedApprovalIsPendingBeforeDispatch(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)
	base.approvalMu.Lock()
	unlock := sync.OnceFunc(base.approvalMu.Unlock)
	defer unlock()
	done := make(chan bool, 1)
	go func() {
		done <- base.ApproveUnpublishedControlRequest(sink, []byte(`{"id":3,"method":"approve"}`), nil, map[string]any{})
	}()
	require.Eventually(t, func() bool {
		base.outstandingMu.Lock()
		defer base.outstandingMu.Unlock()
		record := base.outstandingControls["jsonrpc:3"]
		return record != nil && record.response != nil && record.publishOnFailure
	}, 30*time.Second, time.Millisecond)
	// The unrelated request in approvalProcess must not enter this test's sweep.
	base.outstandingMu.Lock()
	base.outstandingControls["jsonrpc:1"].approveAnswer = nil
	base.outstandingMu.Unlock()
	assert.Zero(t, base.ApproveOutstandingControlRequests(sink, nil))
	unlock()
	require.True(t, <-done)
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.Equal(t, 1, strings.Count(output.String(), `"id":3`))
}

func TestControlApprovalUncertainWriteRetiresWithoutConfirmation(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{Process: Process{stdin: partialStdinWriter{written: 1, err: errors.New("partial write")}}}
	sink := &agenttest.ControlSink{}
	base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"approve"}`), nil, map[string]any{})
	done := make(chan int, 1)
	require.Equal(t, 1, base.ApproveOutstandingControlRequests(sink, func(count int) { done <- count }))
	assert.Zero(t, <-done)
	assert.False(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.Equal(t, []string{"jsonrpc:1"}, sink.CanceledControls())
}

func TestUnpublishedControlApprovalFailurePublishesTheRequest(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{Process: Process{stdin: agenttest.FailingStdin{}}}
	sink := &agenttest.ControlSink{}
	raw := []byte(`{"id":1,"method":"approve"}`)
	require.True(t, base.ApproveUnpublishedControlRequest(sink, raw, nil, map[string]any{}))
	require.Eventually(t, func() bool { return sink.PublishedControlCount() == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, raw, sink.LastPublishedControl().Payload)
	assert.True(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.Empty(t, sink.CanceledControls())
}

func TestControlApprovalReceivesTheWriterReceiptAtShutdown(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	process := &Process{stdinQueue: make(chan stdinFrame, 1), stdinClosed: make(chan struct{})}
	result, err := process.enqueueStdinResult([]byte("approval"))
	require.NoError(t, err)
	process.stdinMu.Lock()
	close(process.stdinClosed)
	process.stdinMu.Unlock()
	go process.runStdinWriter(process.stdinQueue, process.stdinClosed)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, errStdinClosed)
	case <-ctx.Done():
		t.Fatal("shutdown lost the writer receipt")
	}
	_, err = process.enqueueStdinResult([]byte("too late"))
	assert.ErrorIs(t, err, errStdinClosed)
}

type approvalBlockingWriter struct {
	blockingWriter
	entered chan struct{}
	once    sync.Once
}

func (w *approvalBlockingWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.blockingWriter.Write(data)
}

func TestControlApprovalQueueRefusalKeepsTheCard(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	writer := &approvalBlockingWriter{blockingWriter: blockingWriter{release: make(chan struct{})}, entered: make(chan struct{})}
	defer close(writer.release)
	base := &JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(writer)}}
	base.writeStdinDetached([]byte("first\n"), "first")
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the first write did not start")
	}
	for range stdinQueueDepth {
		base.writeStdinDetached([]byte("queued\n"), "queued")
	}
	sink := &agenttest.ControlSink{}
	base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"approve"}`), nil, map[string]any{})
	done := make(chan int, 1)
	queued := make(chan int, 1)
	go func() { queued <- base.ApproveOutstandingControlRequests(sink, func(count int) { done <- count }) }()
	select {
	case count := <-queued:
		require.Equal(t, 1, count)
	case <-ctx.Done():
		t.Fatal("a full queue blocked approval admission")
	}
	select {
	case count := <-done:
		assert.Zero(t, count)
	case <-ctx.Done():
		t.Fatal("a full queue blocked approval completion")
	}
	assert.Empty(t, sink.CanceledControls())
	assert.True(t, base.OutstandingControlForTest("jsonrpc:1"))
}

func TestControlApprovalCannotRaceAReaderAnswer(t *testing.T) {
	t.Parallel()
	writer := newGatedStdin(0, nil)
	release := sync.OnceFunc(func() { close(writer.release) })
	defer release()
	ctx := testutil.DeadlineContext(t)
	base := &JSONRPCProcess{Process: Process{stdin: writer}}
	sink := &agenttest.ControlSink{}
	publication := base.PublishControlRequestWithApproval(sink, []byte(`{"id":1,"method":"approve"}`), nil, map[string]any{})
	queued := make(chan bool, 1)
	go func() { queued <- base.ApproveOutstandingControlRequest(sink, publication) }()
	select {
	case accepted := <-queued:
		require.True(t, accepted)
	case <-ctx.Done():
		t.Fatal("approval admission waited for stdin")
	}
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the approval write did not start")
	}
	assert.Empty(t, sink.CanceledControls())
	err := base.SendRawInputToOutstandingRequest([]byte(`{"id":1,"result":{}}`), agent.StopContext{})
	assert.ErrorIs(t, err, ErrControlRequestNotPending)
	release()
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
}

func TestControlApprovalPreservesSortedOrderBeforeLaterFrames(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	base := &JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(output)}}
	sink := &agenttest.ControlSink{}
	for _, id := range []string{"3", "1", "2"} {
		base.PublishControlRequestWithApproval(sink, []byte(`{"id":`+id+`,"method":"approve"}`), nil, map[string]any{})
	}
	done := make(chan int, 1)
	require.Equal(t, 3, base.ApproveOutstandingControlRequests(sink, func(count int) { done <- count }))
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.Equal(t, 3, <-done)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 4)
	for i, id := range []string{"1", "2", "3", `"marker"`} {
		assert.Contains(t, lines[i], `"id":`+id)
	}
}
