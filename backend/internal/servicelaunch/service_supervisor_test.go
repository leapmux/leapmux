package servicelaunch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceSupervisorReadinessAndFullShutdown(t *testing.T) {
	for _, readyBeforeStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop during startup", true: "stop after readiness"}[readyBeforeStop], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clock := testutil.NewQuartzMock(t)
			created := clock.Trap().NewTicker(serviceProgressTick)
			defer created.Close()
			requests := make(chan serviceLifecycleRequest, 4)
			reports := make(chan serviceProgress, 16)
			notify := make(chan func(), 1)
			closing := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			supervisor := serviceSupervisor{
				clock: clock, requests: requests, report: func(progress serviceProgress) { reports <- progress },
				application: func(appCtx context.Context, ready func()) error {
					notify <- ready
					<-appCtx.Done()
					ready() // A late startup callback must not reverse stop-pending.
					close(closing)
					<-release
					return appCtx.Err()
				},
			}
			done := make(chan error, 1)
			go func() { done <- supervisor.run(ctx) }()
			created.MustWait(ctx).MustRelease(ctx)
			next := func() serviceProgress {
				select {
				case report := <-reports:
					return report
				case <-ctx.Done():
					t.Fatal("service progress did not arrive")
					return serviceProgress{}
				}
			}
			assert.Equal(t, serviceProgress{State: serviceStarting, Checkpoint: 1}, next())
			var ready func()
			select {
			case ready = <-notify:
			case <-ctx.Done():
				t.Fatal("the application did not start")
			}
			clock.Advance(time.Second).MustWait(ctx)
			assert.Equal(t, serviceProgress{State: serviceStarting, Checkpoint: 2}, next())
			if readyBeforeStop {
				ready()
				assert.Equal(t, serviceRunning, next().State)
			}
			requests <- serviceStop
			assert.Equal(t, serviceProgress{State: serviceStopping, Checkpoint: 1}, next())
			select {
			case <-closing:
			case <-ctx.Done():
				t.Fatal("stop did not cancel the application")
			}
			select {
			case err := <-done:
				t.Fatalf("native stop finished before application cleanup: %v", err)
			default:
			}
			clock.Advance(time.Second).MustWait(ctx)
			assert.Equal(t, serviceProgress{State: serviceStopping, Checkpoint: 2}, next())
			releaseOnce.Do(func() { close(release) })
			assert.Equal(t, serviceStopped, next().State)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("native stop did not join application cleanup")
			}
		})
	}
}

func TestServiceSupervisorPreservesApplicationFailure(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	supervisor := serviceSupervisor{
		clock: clock, report: func(serviceProgress) {},
		application: func(context.Context, func()) error { return assert.AnError },
	}
	require.ErrorIs(t, supervisor.run(t.Context()), assert.AnError)
}
