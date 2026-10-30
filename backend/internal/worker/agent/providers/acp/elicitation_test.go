package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeElicitationRequest(t *testing.T, id int, mode string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "elicitation/create",
		"params": map[string]any{"sessionId": "session-1", "mode": mode, "elicitationId": "flow", "url": "https://example.test/login", "message": "Sign in.", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	})
	require.NoError(t, err)
	return raw
}

func countNativeAnswers(requests []agenttest.RecordedRequest) int {
	count := 0
	for _, request := range requests {
		if request.Method == "" {
			count++
		}
	}
	return count
}

func requireURLElicitationCompleted(t *testing.T, base *Base, sink *agenttest.ControlSink) {
	t.Helper()
	testutil.RequireEventually(t, func() bool {
		return base.OutstandingControlCountForTest() == 0 && len(sink.CanceledControls()) == 1
	})
}

func TestURLElicitationCompleteAcceptsAndWithdraws(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, 1, a.OutstandingControlCountForTest())
	complete := []byte(`{"jsonrpc":"2.0","method":"elicitation/complete","params":{"elicitationId":"flow"}}`)
	a.HandleOutput(complete)
	a.HandleOutput(complete)
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	require.Contains(t, answers, "7")
	assert.JSONEq(t, `{"action":"accept"}`, answers["7"])
	assert.Equal(t, 1, countNativeAnswers(requests()))
	assert.Equal(t, []string{sink.PublishedControls()[0].RequestID}, sink.CanceledControls())
	assert.Zero(t, a.OutstandingControlCountForTest())
	assert.Empty(t, sink.Messages())
}

func TestURLElicitationCannotAnswerAnOwnedCancellation(t *testing.T) {
	t.Parallel()
	writer := &cancellationTestStdin{entered: make(chan []byte, 8), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base, _ := newACPTurnBase(t, writer)
	base.SetPromptActiveForTest(true)
	base.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	done := make(chan error, 1)
	go func() {
		_, err := base.CancelConversation(base.CurrentConversation(), func() func(error) { return func(error) {} })
		done <- err
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the cancellation did not enter stdin")
	}
	assert.False(t, base.CompleteURLElicitation("flow", providerkit.MCPElicitationAcceptAnswer()), "the cancellation already owns the only response")
	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the cancellation did not settle")
	}
}

func TestURLElicitationKeepsItsAssociationAfterFailedDelivery(t *testing.T) {
	t.Parallel()
	writer := &cancellationTestStdin{entered: make(chan []byte, 8), release: make(chan struct{}), err: assert.AnError}
	close(writer.release)
	base, _ := newACPTurnBase(t, writer)
	base.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	require.True(t, base.CompleteURLElicitation("flow", providerkit.MCPElicitationAcceptAnswer()))
	select {
	case <-writer.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the completion response did not enter stdin")
	}
	base.Mu.Lock()
	_, retained := base.urlElicitations["flow"]
	base.Mu.Unlock()
	assert.True(t, retained, "a failed delivery must retain the URL association")
	assert.Equal(t, 1, base.OutstandingControlCountForTest())
}

func TestURLElicitationCanRetryAfterDefiniteDeliveryFailure(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	writer := cancellationWriteFunc(func(data []byte) (int, error) {
		if attempts.Add(1) == 1 {
			return 0, assert.AnError
		}
		return len(data), nil
	})
	base, _ := newACPTurnBase(t, agenttest.NopStdin(writer))
	sink := &agenttest.ControlSink{}
	base.sink = agent.NewProviderServices(sink)
	base.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	require.True(t, base.CompleteURLElicitation("flow", providerkit.MCPElicitationAcceptAnswer()))
	testutil.RequireEventually(t, func() bool { return !base.ControlResponsePendingForTest("jsonrpc:7") })
	assert.Equal(t, 1, base.OutstandingControlCountForTest())
	base.Mu.Lock()
	_, retained := base.urlElicitations["flow"]
	base.Mu.Unlock()
	require.True(t, retained)
	require.True(t, base.CompleteURLElicitation("flow", providerkit.MCPElicitationAcceptAnswer()))
	requireURLElicitationCompleted(t, base, sink)
	testutil.RequireEventually(t, func() bool {
		base.Mu.Lock()
		defer base.Mu.Unlock()
		return len(base.urlElicitations) == 0
	})
	assert.Equal(t, int32(2), attempts.Load())
	assert.False(t, base.CompleteURLElicitation("flow", providerkit.MCPElicitationAcceptAnswer()))
}

func TestURLElicitationCompleteUnknownIDDoesNothing(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"unknown"}}`))
	syncTestPeer(t, a)
	assert.Zero(t, countNativeAnswers(requests()))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.CanceledControls())
}

func TestURLElicitationCompleteAfterReaderAnswerSendsNothing(t *testing.T) {
	for _, reused := range []bool{false, true} {
		a, sink, requests := newRetiringTestAgent(t)
		a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
		require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"action":"decline"}}`), agent.StopContext{}))
		if reused {
			a.HandleOutput(nativeElicitationRequest(t, 7, "form"))
		}
		a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
		syncTestPeer(t, a)
		assert.Equal(t, 1, countNativeAnswers(requests()))
		assert.Empty(t, sink.CanceledControls())
		assert.Empty(t, sink.Messages())
		if reused {
			assert.Equal(t, 1, a.OutstandingControlCountForTest())
		} else {
			assert.Zero(t, a.OutstandingControlCountForTest())
		}
	}
}

func TestFormElicitationIsNotRemembered(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "form"))
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	assert.Zero(t, countNativeAnswers(requests()))
	assert.Equal(t, 1, a.OutstandingControlCountForTest())
	assert.Empty(t, sink.CanceledControls())
	assert.Empty(t, sink.Messages())
}

func TestURLElicitationTurnEndCannotAnswerAReusedRequestID(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	a.withdrawTurnControls()
	a.HandleOutput(nativeElicitationRequest(t, 7, "form"))
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	assert.JSONEq(t, `{"action":"cancel"}`, answers["7"])
	assert.Equal(t, 1, countNativeAnswers(requests()))
	assert.Equal(t, 1, a.OutstandingControlCountForTest())
	assert.Len(t, sink.CanceledControls(), 1)
	assert.Empty(t, sink.Messages())
}

func TestURLElicitationRepeatedRequestKeepsItsPublication(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	raw := nativeElicitationRequest(t, 7, "url")
	a.HandleOutput(raw)
	a.HandleOutput(raw)
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, 1, countNativeAnswers(requests()))
	assert.Zero(t, a.OutstandingControlCountForTest())
}

func TestURLElicitationAReusedFlowGetsItsNewPublication(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	require.NoError(t, a.SendRawInput([]byte(`{"id":7,"result":{"action":"decline"}}`), agent.StopContext{}))
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	assert.Len(t, sink.PublishedControls(), 2)
	assert.Equal(t, 2, countNativeAnswers(requests()))
	assert.Zero(t, a.OutstandingControlCountForTest())
}

func TestURLElicitationRejectsAnAbsentRequest(t *testing.T) {
	a, _, _ := newRetiringTestAgent(t)
	assert.NotPanics(t, func() { a.RememberURLElicitation("flow", nil) })
}

func TestURLElicitationForgetsAnsweredFlowsBeforeANewRequest(t *testing.T) {
	a, _, _ := newRetiringTestAgent(t)
	for index := range 5 {
		id := index + 7
		raw := bytes.ReplaceAll(nativeElicitationRequest(t, id, "url"), []byte(`"flow"`), []byte(fmt.Sprintf(`"flow-%d"`, index)))
		a.HandleOutput(raw)
		assert.Len(t, a.urlElicitations, 1)
		require.NoError(t, a.SendRawInput([]byte(fmt.Sprintf(`{"id":%d,"result":{"action":"decline"}}`, id)), agent.StopContext{}))
		assert.Empty(t, a.urlElicitations)
	}
}

func TestURLElicitationKeepsALiveFlowWhenAnotherRequestEnds(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	a.HandleOutput(nativeElicitationRequest(t, 8, "form"))
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":8,"result":{"action":"decline"}}`), agent.StopContext{}))
	assert.Len(t, a.urlElicitations, 1)
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	assert.Equal(t, 2, countNativeAnswers(requests()))
	assert.Len(t, sink.CanceledControls(), 1)
	assert.Zero(t, a.OutstandingControlCountForTest())
}

func TestURLElicitationAcceptsZeroIDWithoutASessionField(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":0,"method":"elicitation/create","params":{"mode":"url","elicitationId":"flow","url":"https://example.test/login","message":"Sign in."}}`))
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	assert.JSONEq(t, `{"action":"accept"}`, answers["0"])
	assert.Len(t, sink.CanceledControls(), 1)
}

func TestURLElicitationForeignSessionCannotReplaceTheOwnedFlow(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	a.HandleOutput(bytes.ReplaceAll(nativeElicitationRequest(t, 8, "url"), []byte("session-1"), []byte("foreign")))
	a.HandleOutput([]byte(`{"method":"elicitation/complete","params":{"elicitationId":"flow"}}`))
	syncTestPeer(t, a)
	requireURLElicitationCompleted(t, &a.Base, sink)
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	assert.JSONEq(t, `{"action":"accept"}`, answers["7"])
	assert.JSONEq(t, `{"action":"cancel"}`, answers["8"])
}

func TestURLElicitationDoesNotUsePermissionBypass(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	bypassed := false
	a.hooks.WithBypassPermission = func(apply func()) { bypassed = true; apply() }
	a.hooks.PermissionAllowsBypass = func(*providerkit.ParsedLine) bool { return true }
	a.HandleOutput(nativeElicitationRequest(t, 7, "url"))
	syncTestPeer(t, a)
	assert.False(t, bypassed)
	assert.Len(t, sink.PublishedControls(), 1)
	assert.Zero(t, countNativeAnswers(requests()))
}
