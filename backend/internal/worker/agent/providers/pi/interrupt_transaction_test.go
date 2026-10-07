package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type piInterruptWriter func([]byte) (int, error)

func (write piInterruptWriter) Write(data []byte) (int, error) { return write(data) }
func (piInterruptWriter) Close() error                         { return nil }

// Reply to sideband statistics without changing the abort writer's sequence.
func setPiInterruptWriter(a *Agent, write piInterruptWriter) {
	a.SetContextForTest(context.Background())
	a.SetStdinForTest(piInterruptWriter(func(data []byte) (int, error) {
		var command struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		if command.Type == CommandGetSessionStats {
			reply, err := json.Marshal(map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": true, "data": nil})
			if err != nil {
				return 0, err
			}
			a.Deliver(command.ID, reply)
			return len(data), nil
		}
		return write(data)
	}))
}

// Output-only fixtures represent a stop whose write completed successfully.
func noteDeliveredPiInterrupt(a *Agent) {
	scope, active, stopped := a.beginPiInterrupt()
	if active && !stopped {
		a.finishPiInterruptWrite(scope, nil)
	}
}

func replyToPiAbort(a *Agent, data []byte) error {
	var command struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &command); err != nil {
		return err
	}
	if command.Type != CommandAbort {
		return errors.New("the test expected an abort command")
	}
	reply, err := json.Marshal(map[string]any{"type": "response", "id": command.ID, "command": CommandAbort, "success": true, "data": nil})
	if err != nil {
		return err
	}
	if !a.Deliver(command.ID, reply) {
		return errors.New("the test abort reply has no waiter")
	}
	return nil
}

func awaitPiInterrupt(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("the Pi interrupt did not finish")
		return nil
	}
}

func awaitPiWrite(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the Pi abort write did not start")
	}
}

func TestPiFailedAbortDoesNotMarkANativeEndInterrupted(t *testing.T) {
	t.Parallel()
	for _, duringWrite := range []bool{false, true} {
		for _, stopReason := range []string{"stop", "error"} {
			t.Run(stopReason+map[bool]string{false: " after write", true: " during write"}[duringWrite], func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.ControlSink{}
				a := newPiAgentWithSink(agent.NewProviderServices(sink))
				a.HandleOutput([]byte(`{"type":"agent_start"}`))
				ended := []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"` + stopReason + `","errorMessage":"The native request failed."}]}`)
				failure := errors.New("the abort write failed")
				setPiInterruptWriter(a, func([]byte) (int, error) {
					if duringWrite {
						// A native event can arrive before the writer returns its result.
						a.HandleOutput(ended)
					}
					return 0, failure
				})
				require.ErrorIs(t, a.Interrupt(agent.StopContext{}), failure)
				if !duringWrite {
					a.HandleOutput(ended)
				}
				rows := sink.Messages()
				require.NotEmpty(t, rows)
				assert.NotEqual(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion,
					"a rejected abort cannot change the native end's outcome")
				assert.False(t, a.takePiInterruptOutcome(piAgentEndEnvelope{}), "the failed abort leaves no accepted intent")
			})
		}
	}
}

func TestPiDeliveredAbortKeepsNativeAbortedOutputBeforeTheWriteReturns(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","stopReason":"aborted","content":[{"type":"text","text":"Cut text"}]}}`))
		a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`))
		if err := replyToPiAbort(a, data); err != nil {
			return 0, err
		}
		return len(data), nil
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestPiOverlappingAbortWritesKeepOnlyAcceptedIntent(t *testing.T) {
	t.Parallel()
	for _, firstSucceeds := range []bool{false, true} {
		t.Run(map[bool]string{false: "both writes fail", true: "the failed write follows an accepted stop"}[firstSucceeds], func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
			releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
			var firstRelease, secondRelease sync.Once
			t.Cleanup(func() {
				firstRelease.Do(func() { close(releaseFirst) })
				secondRelease.Do(func() { close(releaseSecond) })
			})
			var writes atomic.Int32
			failure := errors.New("the abort write failed")
			setPiInterruptWriter(a, func(data []byte) (int, error) {
				if writes.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
					if firstSucceeds {
						if err := replyToPiAbort(a, data); err != nil {
							return 0, err
						}
						return len(data), nil
					}
				} else {
					close(secondStarted)
					<-releaseSecond
				}
				return 0, failure
			})
			firstResult, secondResult := make(chan error, 1), make(chan error, 1)
			go func() { firstResult <- a.Interrupt(agent.StopContext{}) }()
			awaitPiWrite(t, firstStarted)
			go func() { secondResult <- a.Interrupt(agent.StopContext{}) }()
			require.Eventually(t, func() bool { return a.IsPendingForTest("leapmux-2") },
				30*time.Second, time.Millisecond, "the second attempt starts before the first write returns")
			firstRelease.Do(func() { close(releaseFirst) })
			awaitPiWrite(t, secondStarted)
			if firstSucceeds {
				require.NoError(t, awaitPiInterrupt(t, firstResult))
			} else {
				require.ErrorIs(t, awaitPiInterrupt(t, firstResult), failure)
			}
			secondRelease.Do(func() { close(releaseSecond) })
			require.ErrorIs(t, awaitPiInterrupt(t, secondResult), failure)
			a.Mu.Lock()
			if firstSucceeds {
				assert.Len(t, a.interruptRequests, 1, "only the delivered attempt remains")
			} else {
				assert.Empty(t, a.interruptRequests, "both failures leave zero pending or accepted intent")
			}
			a.Mu.Unlock()
			assert.Equal(t, firstSucceeds, a.takePiInterruptOutcome(piAgentEndEnvelope{}), "only the successful write supplies accepted intent")
			assert.False(t, a.takePiInterruptOutcome(piAgentEndEnvelope{}), "consumption leaves no accepted or pending intent")
		})
	}
}

func TestPiUncertainAbortRequiresExactNativeEvidence(t *testing.T) {
	t.Parallel()
	for name, scenario := range map[string]struct {
		reason                    string
		acknowledged, interrupted bool
	}{
		"normal completion":                           {reason: "stop"},
		"length completion":                           {reason: "length"},
		"unrelated native error":                      {reason: "error"},
		"explicit native abort":                       {reason: "aborted", interrupted: true},
		"acknowledged command with normal completion": {reason: "stop", acknowledged: true},
		"acknowledged native abort":                   {reason: "aborted", acknowledged: true, interrupted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "uncertain-test", Ctx: context.Background(), Clock: clock})
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			setPiInterruptWriter(a, func(data []byte) (int, error) {
				a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"` + scenario.reason + `","errorMessage":"An unrelated native error."}]}`))
				if scenario.acknowledged {
					if err := replyToPiAbort(a, data); err != nil {
						return 0, err
					}
				}
				return len(data), errors.New("the writer reported uncertain delivery")
			})
			trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandAbort)
			result := make(chan error, 1)
			go func() { result <- a.Interrupt(agent.StopContext{}) }()
			call, waitErr := trap.Wait(testutil.DeadlineContext(t))
			require.NoError(t, waitErr)
			call.MustRelease(testutil.DeadlineContext(t))
			trap.Close()
			if !scenario.acknowledged {
				a.HandleOutput([]byte(`{"type":"response","id":"leapmux-1","command":"abort","success":false,"error":"The native peer did not confirm the abort."}`))
			}
			err := awaitPiInterrupt(t, result)
			if scenario.acknowledged {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, scenario.interrupted, rows[0].Completion == agent.MessageCompletionInterrupted,
				"only explicit native abort evidence can replace an uncertain outcome")
		})
	}
}

func TestPiOldAbortWritePreservesAReplacementQuestion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	output := &bytes.Buffer{}
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		var command struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		if command.Type != CommandAbort {
			return output.Write(data)
		}
		a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`))
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"replacement-question","toolName":"ask_user_question","args":{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}`))
		a.HandleOutput([]byte(`{"type":"extension_ui_request","id":"replacement-select","method":"select","title":"Choose","options":["1. A — first","2. B — second","3. Type something."]}`))
		if err := replyToPiAbort(a, data); err != nil {
			return 0, err
		}
		return len(data), nil
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}), "a delivered old abort still returns native success")
	assert.Empty(t, sink.CanceledControls(), "the old stop must not cancel the replacement question")
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive)
	assert.Contains(t, a.openDialogs, "replacement-select")
	assert.Contains(t, a.questionDialogs, "replacement-select")
	assert.Empty(t, a.interruptRequests, "the replacement turn inherits no old intent")
	a.Mu.Unlock()
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"replacement-select","value":"My replacement answer"}`), agent.StopContext{}))
	a.HandleOutput([]byte(`{"type":"extension_ui_request","id":"replacement-input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"replacement-select","value":"3. Type something."}`, frames[0])
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"replacement-input","value":"My replacement answer"}`, frames[1])
	assert.Len(t, sink.PublishedControls(), 1)
	a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}]}`))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion)
}

func TestPiLateOldAcknowledgementPreservesALaterStop(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	firstWritten := make(chan struct{})
	var oldCommand []byte
	var writes atomic.Int32
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		if writes.Add(1) == 1 {
			oldCommand = append([]byte(nil), data...)
			close(firstWritten)
		} else if err := replyToPiAbort(a, data); err != nil {
			return 0, err
		}
		return len(data), nil
	})
	oldResult := make(chan error, 1)
	go func() { oldResult <- a.Interrupt(agent.StopContext{}) }()
	awaitPiWrite(t, firstWritten)
	// The first write completed. The response still waits in its own registration.
	require.Eventually(t, func() bool {
		a.interruptOutput.mu.Lock()
		defer a.interruptOutput.mu.Unlock()
		return a.interruptOutput.writes == 0
	}, 30*time.Second, time.Millisecond)
	a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, replyToPiAbort(a, oldCommand))
	require.NoError(t, awaitPiInterrupt(t, oldResult))
	assert.True(t, a.takePiInterruptOutcome(piAgentEndEnvelope{}), "the later stop retains its accepted intent")
	assert.False(t, a.takePiInterruptOutcome(piAgentEndEnvelope{}))
}

func TestPiPendingAbortRoutesItsReplyWhileTheEventQueueIsFull(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	a.interruptOutput.maxFrames = 1
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`))
		frames, _, _ := piOutputQueueState(&a.interruptOutput)
		if frames != 1 {
			return 0, errors.New("the test event queue did not reach its frame limit")
		}
		var command struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		reply, err := json.Marshal(map[string]any{"type": "response", "id": command.ID, "command": CommandAbort, "success": true})
		if err != nil {
			return 0, err
		}
		a.HandleOutput(reply)
		a.Mu.Lock()
		accepted := a.piInterruptDeliveredLocked()
		a.Mu.Unlock()
		if !accepted {
			return 0, errors.New("the matching response did not bypass the full event queue")
		}
		return len(data), nil
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	frames, retained, _ := piOutputQueueState(&a.interruptOutput)
	assert.Zero(t, frames)
	assert.Zero(t, retained)
}
