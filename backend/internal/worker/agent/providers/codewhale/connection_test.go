package codewhale

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestRuntimeEnvPinsTheStoreAndTheToken(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: "/stores/store-1"}
	inherited := []string{
		envRuntimeToken + "=inherited-token",
		envTasksDir + "=/elsewhere/tasks",
		envRuntimeDir + "=/elsewhere/runtime",
		"CODEWHALE_SESSION_ID=parent-session",
		"PATH=/usr/bin",
	}
	env := runtimeEnv(inherited, agent.Options{ExtraEnv: []string{"EXTRA=1"}}, store, "fresh-token")

	assert.Equal(t, []string{"fresh-token"}, envutil.ValuesFor(env, envRuntimeToken), "an inherited token never reaches the runtime")
	assert.Equal(t, []string{store.tasksDir()}, envutil.ValuesFor(env, envTasksDir))
	assert.Equal(t, []string{store.runtimeDir()}, envutil.ValuesFor(env, envRuntimeDir), "an inherited runtime dir would move the store")
	assert.False(t, envutil.HasKey(env, "CODEWHALE_SESSION_ID"))
	assert.True(t, envutil.HasKey(env, "PATH"))
	assert.Contains(t, env, "EXTRA=1")
	assert.Contains(t, env, "LEAPMUX_WORKER=1")
}

// healthRuntime is a runtime that answers /health with 503 until ready reports
// true.
func healthRuntime(t *testing.T, ready *atomic.Bool) *fakeRuntime {
	t.Helper()
	rt := newFakeRuntime(t)
	rt.handle(http.MethodGet, routeHealth, func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	return rt
}

func TestWaitHealthyPollsUntilTheRuntimeAnswers(t *testing.T) {
	t.Parallel()
	var ready atomic.Bool
	rt := healthRuntime(t, &ready)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(healthTimerTag)
	t.Cleanup(newTimer.Close)
	a, _ := newTestAgentWithClock(t, rt, clock)
	ctx := testutil.DeadlineContext(t)

	done := make(chan error, 1)
	go func() { done <- a.waitHealthy(ctx, time.Minute) }()

	// The poll backs off, doubling from its first delay. The runtime turns ready
	// before the last advance, so the poll it wakes is the one that succeeds.
	delays := []time.Duration{healthPollFirst, 2 * healthPollFirst, 4 * healthPollFirst}
	for i, want := range delays {
		call := newTimer.MustWait(ctx)
		assert.Equal(t, want, call.Duration)
		call.MustRelease(ctx)
		if i == len(delays)-1 {
			ready.Store(true)
		}
		clock.Advance(want).MustWait(ctx)
	}
	require.NoError(t, <-done)
	assert.Len(t, rt.requestsTo(http.MethodGet, routeHealth), len(delays)+1)
}

func TestWaitHealthyGivesUpAtTheDeadline(t *testing.T) {
	t.Parallel()
	var ready atomic.Bool
	rt := healthRuntime(t, &ready)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(healthTimerTag)
	t.Cleanup(newTimer.Close)
	a, _ := newTestAgentWithClock(t, rt, clock)
	ctx := testutil.DeadlineContext(t)

	done := make(chan error, 1)
	go func() { done <- a.waitHealthy(ctx, 100*time.Millisecond) }()
	// 20ms, 40ms and 80ms of waiting pass the 100ms deadline, and the poll after
	// the third wait gives up.
	for _, want := range []time.Duration{healthPollFirst, 2 * healthPollFirst, 4 * healthPollFirst} {
		call := newTimer.MustWait(ctx)
		assert.Equal(t, want, call.Duration)
		call.MustRelease(ctx)
		clock.Advance(want).MustWait(ctx)
	}
	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), routeHealth)
}

func TestWaitHealthyStopsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	var ready atomic.Bool
	rt := healthRuntime(t, &ready)
	a, _ := newTestAgent(t, rt)
	a.stopProcess()
	assert.ErrorIs(t, a.waitHealthy(context.Background(), time.Minute), providerkit.ErrServerExited)
}

func TestWaitHealthyStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	var ready atomic.Bool
	rt := healthRuntime(t, &ready)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(healthTimerTag)
	t.Cleanup(newTimer.Close)
	a, _ := newTestAgentWithClock(t, rt, clock)
	deadline := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- a.waitHealthy(ctx, time.Minute) }()
	// The poll waits on its timer. The start that owns the poll ends before the
	// timer fires, and the poll ends with it rather than at its deadline.
	call := newTimer.MustWait(deadline)
	cancel()
	call.MustRelease(deadline)
	assert.ErrorIs(t, <-done, context.Canceled)
	assert.Len(t, rt.requestsTo(http.MethodGet, routeHealth), 1, "no poll follows the end")
}

func TestProcessMatchesRefusesAnIdentityItCannotConfirm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert.False(t, processMatches(ctx, 0, 1, 0))
	assert.False(t, processMatches(ctx, 12345, 0, 0), "a record with no start time identifies nothing")
	created, ok := processIdentity(ctx, int32(os.Getpid()))
	require.True(t, ok)
	assert.True(t, processMatches(ctx, int32(os.Getpid()), created, 0))
	assert.False(t, processMatches(ctx, int32(os.Getpid()), created+1, 0), "a reused pid has another start time")
	assert.False(t, processMatches(ctx, int32(os.Getpid()), created, 65000), "the test binary is not a runtime on that port")
}
