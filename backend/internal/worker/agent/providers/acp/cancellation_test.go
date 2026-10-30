package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergedNativeTurnRestoresItsPromptConversation(t *testing.T) {
	t.Parallel()
	b, _ := newACPTurnBase(t, &agenttest.Stdin{})
	require.NoError(t, b.SendInput("original", nil))
	original := b.CurrentConversation()
	require.NotNil(t, original)
	require.True(t, b.BeginAgentTurn())
	require.NotSame(t, original, b.CurrentConversation())
	b.EndAgentTurnWithoutRowWithVerdict(TurnVerdict{}, MergeQueuedTurnIntoPrompt)
	assert.Same(t, original, b.CurrentConversation())
}

type cancellationTestStdin struct {
	entered chan []byte
	release chan struct{}
	written int
	err     error
}

func (writer *cancellationTestStdin) Write(data []byte) (int, error) {
	writer.entered <- append([]byte(nil), data...)
	<-writer.release
	if writer.err != nil {
		return writer.written, writer.err
	}
	return len(data), nil
}

func (*cancellationTestStdin) Close() error { return nil }

func TestOldConversationCancellationKeepsReplacementURLElicitations(t *testing.T) {
	t.Parallel()
	writer := &cancellationTestStdin{entered: make(chan []byte, 1), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	b, _ := newACPTurnBase(t, writer)
	b.SetPromptActiveForTest(true)
	original := b.CurrentConversation()
	done := make(chan error, 1)
	go func() {
		_, err := b.CancelConversation(original, func() func(error) { return func(error) {} })
		done <- err
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the original cancellation did not enter stdin")
	}
	b.clearActivePrompt()
	b.SetSessionIDForTest("replacement")
	b.SetPromptActiveForTest(true)
	b.Mu.Lock()
	b.urlElicitations = map[string]acpURLElicitation{"new": {requestID: "new", pendingPublication: true}}
	b.Mu.Unlock()
	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the original cancellation did not settle")
	}
	b.Mu.Lock()
	assert.Contains(t, b.urlElicitations, "new")
	b.Mu.Unlock()
	assert.False(t, b.InterruptRequested())
}

func TestQueuedNativeTurnKeepsItsConversationAfterHandoff(t *testing.T) {
	t.Parallel()
	b, _ := newACPTurnBase(t, &agenttest.Stdin{})
	require.NoError(t, b.SendInput("original", nil))
	original := b.CurrentConversation()
	require.True(t, b.BeginAgentTurn())
	queued := b.CurrentConversation()
	b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Same(t, queued, b.CurrentConversation())
	assert.NotSame(t, original, queued)
	admitted, err := b.CancelConversation(original, func() func(error) { t.Error("the stale claim ran"); return func(error) {} })
	require.NoError(t, err)
	assert.False(t, admitted)
}

func TestPromptCompletionKeepsItsOwnConversationCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelOriginal := range []bool{false, true} {
		label := "cancel queued turn"
		if cancelOriginal {
			label = "cancel original turn"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			require.NoError(t, b.SendInput("original", nil))
			b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"original-tool","title":"Read","kind":"read","status":"in_progress"}`))
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "original output"))
			cancel := func() {
				admitted, err := b.CancelConversation(b.CurrentConversation(), func() func(error) { return func(error) {} })
				require.NoError(t, err)
				require.True(t, admitted)
			}
			if cancelOriginal {
				cancel()
			}
			require.True(t, b.BeginAgentTurn())
			if !cancelOriginal {
				cancel()
			}
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "queued output"))
			b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
			b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
			originalText, originalTool := agent.MessageCompletionComplete, agent.MessageCompletionError
			queuedText := agent.MessageCompletionInterrupted
			if cancelOriginal {
				originalText, originalTool = agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted
				queuedText = agent.MessageCompletionComplete
			}
			assert.Equal(t, []agent.MessageCompletion{originalTool}, closingRows(sink, "original-tool"))
			completions := map[string]agent.MessageCompletion{}
			for _, message := range sink.Messages() {
				_, text, completion, ok := agenttest.DecodeAssembledMessage(message.Content)
				if ok {
					completions[text] = completion
				}
			}
			assert.Equal(t, originalText, completions["original output"])
			assert.Equal(t, queuedText, completions["queued output"])
		})
	}
}

type cancellationWriteFunc func([]byte) (int, error)

func (write cancellationWriteFunc) Write(data []byte) (int, error) { return write(data) }

func TestClearContextRefusesToReplaceWorkWhoseCancellationFailed(t *testing.T) {
	t.Parallel()
	var base *Base
	output := &agenttest.Stdin{}
	writer := cancellationWriteFunc(func(data []byte) (int, error) {
		_, _ = output.Write(data)
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		if request.Method == MethodSessionCancel {
			return 0, assert.AnError
		}
		if request.ID != 0 {
			response := json.RawMessage(`{"result":{}}`)
			if request.Method == MethodSessionNew {
				response = json.RawMessage(`{"result":{"sessionId":"replacement"}}`)
			}
			base.Deliver(request.ID, response)
		}
		return len(data), nil
	})
	base, _ = newACPTurnBase(t, agenttest.NopStdin(writer))
	base.SetPromptActiveForTest(true)
	_, err := base.ClearContext()
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, "session-1", base.CurrentSessionID())
	assert.NotContains(t, output.String(), `"method":"session/new"`)
	assert.True(t, base.PromptActive())
}

func TestPromptWithUncertainDeliveryRetainsItsCompletion(t *testing.T) {
	t.Parallel()
	for label, send := range map[string]func(*Base) error{
		"ordinary input": func(base *Base) error { return base.SendInput("input", nil) },
		"continued input": func(base *Base) error {
			base.SetPromptActiveForTest(true)
			return base.continueWithPrompt("session-1", "input", nil)
		},
		"scheduled input": func(base *Base) error {
			base.SetPromptActiveForTest(true)
			base.sendFollowUpPrompt("session-1", "input", nil)
			return nil
		},
	} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			writer := cancellationWriteFunc(func(data []byte) (int, error) { return len(data), assert.AnError })
			base, sink := newACPTurnBase(t, agenttest.NopStdin(writer))
			err := send(base)
			if label != "scheduled input" {
				assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			require.True(t, base.PromptActive(), "uncertain delivery must not admit another prompt")
			base.HandleJSONRPCResponseForTest(promptResponse(1))
			testutil.RequireEventually(t, func() bool { return !base.PromptActive() })
			assert.Len(t, turnEndContents(sink), 1, "the retained response closes the original prompt once")
		})
	}
}

func TestFailedUserCancellationKeepsItsControlsAnswerable(t *testing.T) {
	t.Parallel()
	base, _ := newACPTurnBase(t, agenttest.FailingStdin{})
	sink := &agenttest.ControlSink{}
	base.sink = agent.NewProviderServices(sink)
	base.SetPromptActiveForTest(true)
	base.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{"sessionId":"session-1"}}`))
	require.Len(t, sink.PublishedControls(), 1)
	err := base.Interrupt(agent.StopContext{})
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, base.OutstandingControlCountForTest(), "no response reached the provider, so its request remains answerable")
	assert.Empty(t, sink.CanceledControls())
	assert.True(t, base.InterruptRequested(), "the user's stop intent still suppresses retries")
}

func TestPromptCompletionWaitsForItsCancellationReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label   string
		written int
		err     error
		stopped bool
	}{
		{label: "complete write", stopped: true},
		{label: "zero-byte failure", err: assert.AnError},
		{label: "uncertain write", written: 1, err: assert.AnError, stopped: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			writer := &cancellationTestStdin{entered: make(chan []byte, 1), release: make(chan struct{}), written: tc.written, err: tc.err}
			unblock := sync.OnceFunc(func() { close(writer.release) })
			t.Cleanup(unblock)
			base, _ := newACPTurnBase(t, writer)
			base.SetPromptActiveForTest(true)
			var settled atomic.Bool
			cancelDone := make(chan error, 1)
			go func() {
				_, err := base.CancelConversation(base.CurrentConversation(), func() func(error) { return func(error) { settled.Store(true) } })
				cancelDone <- err
			}()
			ctx := testutil.DeadlineContext(t)
			select {
			case <-writer.entered:
			case <-ctx.Done():
				t.Fatal("the cancellation did not enter stdin")
			}
			ended := make(chan bool, 1)
			base.hooks.PromptEnded = func(_ error, stopped bool) PromptEnding {
				assert.True(t, settled.Load(), "provider state must settle before the verdict")
				ended <- stopped
				return PromptEnding{}
			}
			finishDone := make(chan struct{})
			go func() {
				defer close(finishDone)
				base.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
			}()
			testutil.RequireEventually(t, func() bool {
				if base.sessionMu.TryLock() {
					base.sessionMu.Unlock()
					return false
				}
				return true
			})
			select {
			case <-ended:
				t.Fatal("the prompt decided its verdict before cancellation settled")
			default:
			}
			// The callback holds no output lock while it waits for stdin.
			base.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "reader remains live"))
			unblock()
			select {
			case err := <-cancelDone:
				if tc.err != nil {
					assert.ErrorIs(t, err, tc.err)
					assert.Equal(t, tc.written > 0, errors.Is(err, agent.ErrDeliveryUncertain))
				} else {
					require.NoError(t, err)
				}
			case <-ctx.Done():
				t.Fatal("cancellation did not settle")
			}
			select {
			case stopped := <-ended:
				assert.Equal(t, tc.stopped, stopped)
			case <-ctx.Done():
				t.Fatal("the settled prompt did not decide its verdict")
			}
			select {
			case <-finishDone:
			case <-ctx.Done():
				t.Fatal("the settled prompt did not finish")
			}
			assert.False(t, base.PromptActive())
		})
	}
}

func TestUserStopSurvivesAConcurrentFailedInternalCancellation(t *testing.T) {
	t.Parallel()
	writer := &cancellationTestStdin{entered: make(chan []byte, 8), release: make(chan struct{}), err: assert.AnError}
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base, _ := newACPTurnBase(t, writer)
	sink := &agenttest.ControlSink{}
	base.sink = agent.NewProviderServices(sink)
	base.SetPromptActiveForTest(true)
	base.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{"sessionId":"session-1"}}`))
	internalDone := make(chan error, 1)
	go func() {
		_, err := base.CancelConversation(base.CurrentConversation(), func() func(error) { return func(error) {} })
		internalDone <- err
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the internal cancellation did not enter stdin")
	}
	userDone := make(chan error, 1)
	go func() { userDone <- base.Interrupt(agent.StopContext{}) }()
	testutil.RequireEventually(t, func() bool {
		base.Mu.Lock()
		defer base.Mu.Unlock()
		return base.interruptRequested
	})
	unblock()
	for _, done := range []<-chan error{internalDone, userDone} {
		select {
		case err := <-done:
			assert.ErrorIs(t, err, assert.AnError)
		case <-ctx.Done():
			t.Fatal("a cancellation did not settle")
		}
	}
	assert.True(t, base.InterruptRequested())
	assert.Equal(t, 1, base.OutstandingControlCountForTest())
	assert.Empty(t, sink.CanceledControls())
}

func TestUserAnswerCannotBypassAnOwnedCancellationResponse(t *testing.T) {
	t.Parallel()
	writer := &cancellationTestStdin{entered: make(chan []byte, 8), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(unblock)
	base, _ := newACPTurnBase(t, writer)
	base.SetPromptActiveForTest(true)
	base.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{"sessionId":"session-1"}}`))
	userFramed := make(chan struct{})
	base.FrameMessage = func(data []byte) []byte {
		if bytes.Contains(data, []byte(`"choice":"user"`)) {
			close(userFramed)
		}
		return append(append([]byte(nil), data...), '\n')
	}
	cancelDone := make(chan error, 1)
	go func() {
		_, err := base.CancelConversation(base.CurrentConversation(), func() func(error) { return func(error) {} })
		cancelDone <- err
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-writer.entered:
	case <-ctx.Done():
		t.Fatal("the cancellation did not enter stdin")
	}
	userDone := make(chan error, 1)
	go func() {
		userDone <- base.SendRawInput([]byte(`{"id":11,"result":{"choice":"user"}}`), agent.StopContext{})
	}()
	select {
	case err := <-userDone:
		assert.ErrorIs(t, err, providerkit.ErrControlRequestNotPending)
	case <-userFramed:
		t.Error("the user answer bypassed the cancellation's response ownership")
	case <-ctx.Done():
		t.Fatal("the competing user answer did not finish")
	}
	unblock()
	select {
	case err := <-cancelDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the cancellation did not settle")
	}
}
