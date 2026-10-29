//go:build unix

package opencodetest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputFileTurnSinkRetriesRetainedTurnEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	sink := newOutputFileTurnSink(ctx, clock)
	queue := sink.OutputQueue()
	t.Cleanup(queue.StopRetries)
	created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	var allow atomic.Bool
	queue.Append(agent.OutputWrite{Label: "registry publication", Run: func() error {
		if !allow.Load() {
			return assert.AnError
		}
		return nil
	}}, agent.OutputWrite{Label: "turn end", Run: func() error {
		return sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, agent.SpanInfo{})
	}})
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	require.True(t, queue.HasPending())
	delay := testutil.WaitForTimer(t, ctx, created)
	allow.Store(true)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("the retained turn end did not reach the output sink")
	}
	queue.StopRetries()
	assert.False(t, queue.HasPending())
	assert.Len(t, sink.Messages(), 1)
}
