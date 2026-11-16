package kimi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiMainStateFrame = `{"type":"agent","session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-k2","thinking":"high","permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`

func newKimiPushStateStream(t *testing.T) *kimiStream {
	t.Helper()
	stream := newKimiStream(nil, "test-agent", quartz.NewReal(), nil, nil)
	stream.sessions["session_1"] = kimiCursor{}
	t.Cleanup(stream.close)
	return stream
}

func observeKimiState(t *testing.T, stream *kimiStream, raw string) {
	t.Helper()
	var frame kimiFrame
	require.NoError(t, json.Unmarshal([]byte(raw), &frame))
	stream.observeSessionState(frame)
}

func TestKimiSessionStatusKeepsZeroUsageAndMainAgentOwnership(t *testing.T) {
	t.Parallel()
	stream := newKimiPushStateStream(t)
	observeKimiState(t, stream, kimiMainStateFrame)
	observeKimiState(t, stream, `{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":0}`)
	for _, raw := range []string{
		`{"type":"agent","session_id":"session_1","agent_id":"child","status":"running","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","session_id":"session_other","agent_id":"main","status":"running","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","session_id":"session_1","agent_id":"main","status":"unknown","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","session_id":"session_1","agent_id":"main","status":"running","model":"other"}`,
		`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":-1}`,
		`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":9,"max_context_tokens":0}`,
		`{"type":"usage","session_id":"session_1","agent_id":"child","context_tokens":9,"max_context_tokens":99}`,
	} {
		observeKimiState(t, stream, raw)
	}
	status, exists := stream.sessionStatus("session_1")
	require.True(t, exists)
	assert.Equal(t, kimiStatus{
		HasContextUsage: true, Model: "kimi-k2", ThinkingLevel: "high", Permission: "manual",
	}, status)
	_, exists = stream.sessionStatus("session_other")
	assert.False(t, exists)
}

func TestKimiSessionStatusWaitEndsWhenTheStreamCloses(t *testing.T) {
	t.Parallel()
	stream := newKimiPushStateStream(t)
	observeKimiState(t, stream, kimiMainStateFrame)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := stream.waitSessionStatus(ctx, "session_1", func(kimiStatus) bool {
			close(waiting)
			return false
		})
		done <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("the status waiter did not examine the native state")
	}
	stream.close()
	select {
	case err := <-done:
		require.ErrorIs(t, err, errKimiStreamClosed)
	case <-ctx.Done():
		t.Fatal("closing the stream did not release its status waiter")
	}
}

func TestKimiStreamDropsCachedStateOnClose(t *testing.T) {
	t.Parallel()
	stream := newKimiPushStateStream(t)
	observeKimiState(t, stream, kimiMainStateFrame)
	stream.close()
	_, exists := stream.sessionStatus("session_1")
	assert.False(t, exists)
}
