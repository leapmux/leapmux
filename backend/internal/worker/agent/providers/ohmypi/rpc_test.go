package ohmypi

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendCommandReturnsTheData(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		return &rigReply{Data: json.RawMessage(`{"levels":["off","high"]}`)}
	})
	data, err := r.agent.sendCommand("get_available_thinking_levels", map[string]any{"extra": 1}, 10*time.Second)
	require.NoError(t, err)
	assert.JSONEq(t, `{"levels":["off","high"]}`, string(data))

	command := r.commandsOfType("get_available_thinking_levels")[0]
	assert.Equal(t, "leapmux-1", command.ID, "the worker's ids cannot collide with omp's own")
	assert.Equal(t, float64(1), command.Payload["extra"])
}

func TestSendCommandReturnsOmpsRefusal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		return &rigReply{Error: "Model not found: mock/x"}
	})
	_, err := r.agent.sendCommand(CommandSetModel, nil, 10*time.Second)
	var refusal *commandError
	require.True(t, errors.As(err, &refusal))
	assert.Equal(t, CommandSetModel, refusal.Command)
	assert.Equal(t, "omp set_model failed: Model not found: mock/x", err.Error())
	assert.Equal(t, "omp abort failed", (&commandError{Command: "abort"}).Error(), "a refusal with no message still reads")
}

func TestSendCommandKeepsTheRefusalCode(t *testing.T) {
	t.Parallel()
	_, err := parseResponse(CommandPrompt, json.RawMessage(`{"type":"response","id":"x","command":"prompt","success":false,"error":"busy","code":"agent_busy"}`))
	var refusal *commandError
	require.True(t, errors.As(err, &refusal))
	assert.Equal(t, "agent_busy", refusal.Code)

	_, err = parseResponse(CommandPrompt, json.RawMessage(`{`))
	assert.ErrorContains(t, err, "decode omp prompt response")
}

func TestSendCommandOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.SetStoppedForTest(true)
	_, err := r.agent.sendCommand(CommandGetState, nil, time.Second)
	assert.ErrorContains(t, err, "agent is stopped")
	assert.Empty(t, r.commandsOfType(CommandGetState), "nothing is written")
}

func TestSendCommandFailsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply { return &rigReply{Skip: true} })
	done := make(chan error, 1)
	go func() {
		_, err := r.agent.sendCommand(CommandGetState, nil, 0)
		done <- err
	}()
	r.waitForCommand(CommandGetState, 1)
	require.NoError(t, r.stdinR.Close())
	select {
	case err := <-done:
		assert.Error(t, err, "a caller never waits for a process that is gone")
	case <-time.After(30 * time.Second):
		t.Fatal("the caller still waits after the exit")
	}
}

func TestSendCommandDetachedHandsTheOutcomeOver(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	outcome := make(chan json.RawMessage, 1)
	require.NoError(t, r.agent.sendCommandDetached(CommandGetSessionStats, nil, func(data json.RawMessage, err error) {
		assert.NoError(t, err)
		outcome <- data
	}))
	select {
	case data := <-outcome:
		assert.Empty(t, data)
	case <-time.After(30 * time.Second):
		t.Fatal("the outcome never arrived")
	}
}

func TestSendCommandDetachedOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.SetStoppedForTest(true)
	var called atomic.Bool
	err := r.agent.sendCommandDetached(CommandAbort, nil, func(json.RawMessage, error) { called.Store(true) })
	assert.ErrorContains(t, err, "agent is stopped")
	assert.False(t, called.Load(), "the caller hears of the failure; the handler never runs")
	assert.Empty(t, r.commandsOfType(CommandAbort))
}

// The ids that the worker mints never repeat, so no response can reach the
// caller of another command.
func TestCommandIDsAreUnique(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	const commands = 20
	done := make(chan error, commands)
	for range commands {
		go func() {
			_, err := r.agent.sendCommand(CommandGetState, nil, 30*time.Second)
			done <- err
		}()
	}
	for range commands {
		require.NoError(t, <-done)
	}
	seen := make(map[string]bool, commands)
	for _, command := range r.commandsOfType(CommandGetState) {
		assert.False(t, seen[command.ID], "the id %q repeats", command.ID)
		seen[command.ID] = true
	}
	assert.Len(t, seen, commands)
}

func TestAResponseWithNoCallerReachesTheDispatcher(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"response","id":"leapmux-99","command":"get_state","success":true}`, `{"type":"response","command":"abort","success":true}`)
	assert.Empty(t, r.sink.Messages(), "an orphan response is logged, not persisted")
	assert.Empty(t, r.sink.Notifications())
}

func TestReadyFrameSupports(t *testing.T) {
	t.Parallel()
	assert.True(t, readyFrame{SupportedProtocolVersions: []int{1, 2}}.supports(2))
	assert.False(t, readyFrame{SupportedProtocolVersions: []int{1}}.supports(2))
	assert.False(t, readyFrame{}.supports(1))
}

// A timed command waits on the agent's clock. An answer inside the limit is the
// data. An exit inside the limit is the exit, not a timeout, and the limit ends
// a wait that nothing else ends.
func TestSendCommandWaitsOnTheAgentsClock(t *testing.T) {
	t.Parallel()

	t.Run("an answer inside the limit returns the data", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		ctx := testutil.DeadlineContext(t)
		wait := r.clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandGetState)
		defer wait.Close()
		release := make(chan struct{})
		r.respond(func(recordedCommand) *rigReply {
			<-release
			return &rigReply{Data: json.RawMessage(`{"sessionId":"s1"}`)}
		})
		result := make(chan error, 1)
		go func() {
			_, err := r.agent.sendCommand(CommandGetState, nil, 3*time.Second)
			result <- err
		}()
		assert.Equal(t, 3*time.Second, testutil.WaitForTimer(t, ctx, wait))
		r.clock.Advance(3*time.Second - time.Nanosecond).MustWait(ctx)
		close(release)
		select {
		case err := <-result:
			assert.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("the answer did not end the wait")
		}
		_, pending := r.clock.Peek()
		assert.False(t, pending, "the answer stops the wait's timer")
	})

	t.Run("the limit ends a wait that nothing else ends", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		ctx := testutil.DeadlineContext(t)
		wait := r.clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandGetState)
		defer wait.Close()
		r.respond(func(recordedCommand) *rigReply { return &rigReply{Skip: true} })
		result := make(chan error, 1)
		go func() {
			_, err := r.agent.sendCommand(CommandGetState, nil, 3*time.Second)
			result <- err
		}()
		r.clock.Advance(testutil.WaitForTimer(t, ctx, wait)).MustWait(ctx)
		select {
		case err := <-result:
			assert.EqualError(t, err, "timeout waiting for get_state response")
		case <-ctx.Done():
			t.Fatal("the limit did not end the wait")
		}
	})

	t.Run("an exit inside the limit is the exit", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		ctx := testutil.DeadlineContext(t)
		wait := r.clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandGetState)
		defer wait.Close()
		r.respond(func(recordedCommand) *rigReply { return &rigReply{Skip: true} })
		result := make(chan error, 1)
		go func() {
			_, err := r.agent.sendCommand(CommandGetState, nil, 3*time.Second)
			result <- err
		}()
		testutil.WaitForTimer(t, ctx, wait)
		require.NoError(t, r.stdinR.Close())
		select {
		case err := <-result:
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "timeout", "the exit states why no answer comes")
		case <-ctx.Done():
			t.Fatal("the exit did not end the wait")
		}
	})
}
