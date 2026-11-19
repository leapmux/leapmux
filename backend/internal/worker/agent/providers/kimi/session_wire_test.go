package kimi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiMainStateFrame = `{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-k2","thinking":"high","permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`

type kimiStateWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *kimiStateWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestKimiSessionStatusWaitsForStateAfterSubscriptionAcknowledgement(t *testing.T) {
	t.Parallel()
	stream := newKimiPushStateStream(t)
	ctx := &kimiStateWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	type result struct {
		status kimiStatus
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, err := stream.waitSessionStatus(ctx, "session_1", func(kimiStatus) bool { return true })
		done <- result{status, err}
	}()
	select {
	case <-ctx.waiting:
	case premature := <-done:
		t.Fatalf("the subscription acknowledged before state, but the waiter returned: %v", premature.err)
	case <-t.Context().Done():
		t.Fatal("the subscription state waiter did not begin")
	}
	observeKimiState(t, stream, kimiMainStateFrame)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, "kimi-k2", got.status.Model)
	case <-t.Context().Done():
		t.Fatal("the main-agent state did not release the waiter")
	}
}

func TestKimiReadStatusWaitsForAnAlreadyTrackedSubscription(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	stream := newKimiPushStateStream(t)
	rig.agent.stream = stream
	ctx := &kimiStateWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	type result struct {
		status kimiStatus
		err    error
	}
	done := make(chan result, 1)
	go func() { status, err := rig.agent.readStatus(ctx, "session_1"); done <- result{status, err} }()
	select {
	case <-ctx.waiting:
	case premature := <-done:
		t.Fatalf("the tracked subscription must supply state without another request: %v", premature.err)
	case <-t.Context().Done():
		t.Fatal("the tracked-state wait did not start")
	}
	observeKimiState(t, stream, kimiMainStateFrame)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, "kimi-k2", got.status.Model)
	case <-t.Context().Done():
		t.Fatal("the tracked subscription did not supply state")
	}
}

func newKimiPushStateStream(t *testing.T) *kimiStream {
	t.Helper()
	stream := newKimiStream(nil, "test-agent", quartz.NewReal(), nil, nil)
	stream.sessions["session_1"] = kimiSubscription{}
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
		`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"child","status":"running","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","compacting":false,"session_id":"session_other","agent_id":"main","status":"running","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"unknown","model":"other","plan_mode":true,"swarm_mode":true,"tower_mode":false}`,
		`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"running","model":"other"}`,
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
