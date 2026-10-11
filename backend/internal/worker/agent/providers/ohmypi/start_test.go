package ohmypi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const frameReady = `{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2],"maxFrameBytes":1048576,"maxReassembledFrameBytes":67108864}`

func TestLaunchArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options optionmap.Map
		resume  []string
		want    []string
	}{
		{
			name: "the defaults",
			want: []string{"--mode", "rpc-ui", "--cwd", "/work", "--approval-mode", "write"},
		},
		{
			name:    "every option",
			options: optionmap.Map{agent.OptionIDModel: "anthropic/claude-sonnet-4-5", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: "always-ask"},
			resume:  []string{"--resume", "/s/a.jsonl"},
			want:    []string{"--mode", "rpc-ui", "--cwd", "/work", "--model", "anthropic/claude-sonnet-4-5", "--thinking", "high", "--approval-mode", "always-ask", "--resume", "/s/a.jsonl"},
		},
		{
			name:    "Auto sends no level",
			options: optionmap.Map{agent.OptionIDEffort: agent.EffortAuto},
			want:    []string{"--mode", "rpc-ui", "--cwd", "/work", "--approval-mode", "write"},
		},
		{
			name:    "the default-model sentinel sends no model",
			options: optionmap.Map{agent.OptionIDModel: agent.DefaultModelSentinel},
			want:    []string{"--mode", "rpc-ui", "--cwd", "/work", "--approval-mode", "write"},
		},
		{
			name:    "an unknown approval mode falls back",
			options: optionmap.Map{agent.OptionIDPermissionMode: "bypassPermissions"},
			want:    []string{"--mode", "rpc-ui", "--cwd", "/work", "--approval-mode", "write"},
		},
		{
			name:    "yolo",
			options: optionmap.Map{agent.OptionIDPermissionMode: "yolo"},
			want:    []string{"--mode", "rpc-ui", "--cwd", "/work", "--approval-mode", "yolo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := launchArgs(agent.Options{WorkingDir: "/work", Options: tc.options}, tc.resume)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHandshake(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		switch command.Type {
		case CommandGetState:
			return &rigReply{Data: json.RawMessage(`{"model":{"id":"mock-model","provider":"mock"},"thinkingLevel":"medium","sessionId":"s2","sessionFile":"/sessions/s2.jsonl"}`)}
		case CommandGetAvailableModels:
			return &rigReply{Data: json.RawMessage(availableModels)}
		}
		return nil
	})
	r.emit(frameReady)
	require.NoError(t, r.agent.handshake(10*time.Second))

	var order []string
	for _, command := range r.commandsOfType(CommandNegotiateProtocol) {
		assert.Equal(t, float64(2), command.Payload["protocolVersion"])
	}
	r.mu.Lock()
	for _, command := range r.commands {
		order = append(order, command.Type)
	}
	r.mu.Unlock()
	assert.Equal(t, []string{CommandNegotiateProtocol, CommandSetSubagentSubscription, CommandGetState, CommandGetAvailableModels}, order)
	assert.Equal(t, "events", r.commandsOfType(CommandSetSubagentSubscription)[0].Payload["level"])

	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	assert.Equal(t, "medium", r.agent.thinkingLevel)
	assert.Equal(t, "medium", r.agent.effectiveThinking)
	assert.Equal(t, "/sessions/s2.jsonl", r.agent.sessionHandleLocked())
	assert.Len(t, r.agent.availableModels, 2)
	assert.Equal(t, 67108864, r.agent.chunks.limit, "the ready frame sets the reassembly limit")
}

func TestHandshakeToleratesOptionalFailures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		switch command.Type {
		case CommandNegotiateProtocol, CommandSetSubagentSubscription, CommandGetAvailableModels:
			return &rigReply{Error: "unknown command"}
		}
		return nil
	})
	r.emit(frameReady)
	assert.NoError(t, r.agent.handshake(10*time.Second), "an old omp without these commands still starts")
}

func TestHandshakeSkipsANegotiationOmpDoesNotOffer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1]}`)
	require.NoError(t, r.agent.handshake(10*time.Second))
	assert.Empty(t, r.commandsOfType(CommandNegotiateProtocol))
}

func TestHandshakeFailsWithoutTheState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandGetState {
			return &rigReply{Error: "no session"}
		}
		return nil
	})
	r.emit(frameReady)
	err := r.agent.handshake(10 * time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session")
}

func TestAwaitReady(t *testing.T) {
	t.Parallel()

	t.Run("takes only the first ready frame", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameReady, `{"type":"ready","protocolVersion":9}`)
		ready, err := r.agent.awaitReady(10 * time.Second)
		require.NoError(t, err)
		assert.True(t, ready.supports(2))
		assert.False(t, ready.supports(9))
	})

	t.Run("an exit before the frame is the answer", func(t *testing.T) {
		r := newRig(t)
		require.NoError(t, r.stdinW.Close())
		_, err := r.agent.awaitReady(10 * time.Second)
		assert.Error(t, err)
	})

	t.Run("no frame at all times out", func(t *testing.T) {
		r := newRig(t)
		ctx := testutil.DeadlineContext(t)
		wait := r.clock.Trap().NewTimer(ompReadyTimerTag)
		defer wait.Close()
		result := make(chan error, 1)
		go func() {
			_, err := r.agent.awaitReady(10 * time.Second)
			result <- err
		}()
		assert.Equal(t, 10*time.Second, testutil.WaitForTimer(t, ctx, wait), "the wait is the timeout that the caller gives")
		r.clock.Advance(10*time.Second - time.Nanosecond).MustWait(ctx)
		select {
		case err := <-result:
			t.Fatalf("the wait ended before its timeout: %v", err)
		default:
		}
		r.clock.Advance(time.Nanosecond).MustWait(ctx)
		select {
		case err := <-result:
			assert.ErrorContains(t, err, "no ready frame within 10s")
		case <-ctx.Done():
			t.Fatal("the wait did not end at its timeout")
		}
	})

	t.Run("a cancelled start ends the wait", func(t *testing.T) {
		r := newRig(t)
		r.agent.CancelForTest()
		// The mock clock never ends the wait: only the cancel may.
		_, err := r.agent.awaitReady(10 * time.Second)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
