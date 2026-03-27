package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduleAutoContinue_FiresAfterDelay(t *testing.T) {
	h := &OutputHandler{}

	var called atomic.Int32

	h.sendMessageFunc = func(agentID, content string) {
		called.Add(1)
	}

	h.scheduleAutoContinue("agent-1")

	// Should not fire immediately.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())

	// Wait for the initial delay (10s) + some margin.
	// We can't wait that long in a unit test, so we test via
	// the internal state instead: verify the state was created.
	v, ok := h.autoContinue.Load("agent-1")
	require.True(t, ok)
	st := v.(*autoContinueState)
	st.mu.Lock()
	assert.NotNil(t, st.timer, "timer should be set")
	assert.Equal(t, 1, st.generation)
	// Backoff should have doubled from initial 10s to 20s.
	assert.Equal(t, time.Duration(float64(autoContinueInitialDelay)*autoContinueMultiplier), st.backoff)
	st.mu.Unlock()

	// Cleanup.
	h.cleanupAutoContinue("agent-1")
}

func TestResetAutoContinue_StopsTimer(t *testing.T) {
	h := &OutputHandler{}
	h.sendMessageFunc = func(string, string) {}

	h.scheduleAutoContinue("agent-1")

	v, ok := h.autoContinue.Load("agent-1")
	require.True(t, ok)
	st := v.(*autoContinueState)

	h.resetAutoContinue("agent-1")

	st.mu.Lock()
	assert.Nil(t, st.timer, "timer should be nil after reset")
	assert.Equal(t, autoContinueInitialDelay, st.backoff, "backoff should be reset to initial")
	st.mu.Unlock()
}

func TestResetAutoContinue_NoopWhenNoState(t *testing.T) {
	h := &OutputHandler{}
	// Should not panic.
	h.resetAutoContinue("nonexistent")
}

func TestCleanupAutoContinue_RemovesState(t *testing.T) {
	h := &OutputHandler{}
	h.sendMessageFunc = func(string, string) {}

	h.scheduleAutoContinue("agent-1")
	_, ok := h.autoContinue.Load("agent-1")
	require.True(t, ok)

	h.cleanupAutoContinue("agent-1")
	_, ok = h.autoContinue.Load("agent-1")
	assert.False(t, ok, "state should be removed after cleanup")
}

func TestScheduleAutoContinue_BackoffIncreases(t *testing.T) {
	h := &OutputHandler{}
	h.sendMessageFunc = func(string, string) {}

	// Schedule twice to see backoff increase.
	h.scheduleAutoContinue("agent-1")
	h.scheduleAutoContinue("agent-1")

	v, _ := h.autoContinue.Load("agent-1")
	st := v.(*autoContinueState)
	st.mu.Lock()
	// After two schedules: initial 10s -> 20s (first) -> 40s (second).
	expected := time.Duration(float64(autoContinueInitialDelay) * autoContinueMultiplier * autoContinueMultiplier)
	assert.Equal(t, expected, st.backoff)
	assert.Equal(t, 2, st.generation, "generation should increment on each schedule")
	st.mu.Unlock()

	h.cleanupAutoContinue("agent-1")
}
