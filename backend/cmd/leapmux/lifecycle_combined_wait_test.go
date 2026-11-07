package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/solo"
)

type combinedWaitFixture struct {
	done    chan struct{}
	changed chan struct{}
	failure error
	stops   int
}

func (f *combinedWaitFixture) ObserveStartup() (solo.StartupSnapshot, <-chan struct{}) {
	return solo.StartupSnapshot{WorkerReady: true}, f.changed
}
func (f *combinedWaitFixture) Startup() solo.StartupSnapshot {
	return solo.StartupSnapshot{WorkerReady: f.failure == nil, Failure: f.failure, Connection: lifecycle.ConnectionStatus{State: lifecycle.ConnectionDisconnected}}
}
func (f *combinedWaitFixture) WaitDone() <-chan struct{} { return f.done }
func (f *combinedWaitFixture) Stop() error               { f.stops++; return f.failure }

func TestCombinedForegroundStopsAndReportsFailureOnce(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleSolo, DataDir: t.TempDir(), Execution: lifecycle.ExecutionForeground})
			require.NoError(t, err)
			t.Cleanup(func() { _ = runtime.Finish(nil) })
			fixture := &combinedWaitFixture{done: make(chan struct{}), changed: make(chan struct{})}
			if failed {
				fixture.failure = errors.New("combined instance failure")
			}
			ctx, cancel := context.WithCancel(t.Context())
			if canceled {
				cancel()
			} else {
				close(fixture.done)
			}
			err = waitCombinedForeground(ctx, runtime, fixture)
			cancel()
			if failed {
				require.ErrorIs(t, err, fixture.failure)
				assert.Equal(t, 1, strings.Count(err.Error(), fixture.failure.Error()))
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 1, fixture.stops)
			assert.Equal(t, lifecycle.ConnectionDisconnected, runtime.Status().Connection.State, "publish the joined snapshot")
			require.NoError(t, runtime.Finish(nil))
		}
	}
}
