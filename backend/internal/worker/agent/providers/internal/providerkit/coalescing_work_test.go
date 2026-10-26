package providerkit

import (
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoalescingWorkCombinesRequestsDuringAnActiveRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var work CoalescingWork
		var runs atomic.Int64
		release := make(chan struct{})
		callback := func() {
			if runs.Add(1) == 1 {
				<-release
			}
		}
		work.Run("approvals", callback)
		synctest.Wait()
		for range 100 {
			work.Run("approvals", callback)
		}
		require.Equal(t, map[string]bool{"approvals": true}, work.InFlight())
		snapshot := work.InFlight()
		delete(snapshot, "approvals")
		assert.False(t, work.Idle(), "the snapshot cannot change the work state")
		close(release)
		synctest.Wait()
		assert.Equal(t, int64(2), runs.Load())
		assert.True(t, work.Idle())
		work.Run("approvals", callback)
		synctest.Wait()
		assert.Equal(t, int64(3), runs.Load())
	})
}

func TestCoalescingWorkRunsIndependentKeysSeparately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var work CoalescingWork
		release := make(chan struct{})
		work.Run("blocked", func() { <-release })
		var ran atomic.Bool
		work.Run("independent", func() { ran.Store(true) })
		synctest.Wait()
		assert.True(t, ran.Load())
		assert.Equal(t, map[string]bool{"blocked": false}, work.InFlight())
		close(release)
		synctest.Wait()
		assert.True(t, work.Idle())
	})
}

func TestCoalescingWorkClosureJoinsAndRejectsFurtherWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var work CoalescingWork
		var runs atomic.Int64
		release := make(chan struct{})
		callback := func() { runs.Add(1); <-release }
		work.Run("blocked", callback)
		synctest.Wait()
		work.Run("blocked", callback)
		closed := make(chan struct{})
		go func() { work.CloseAndWait(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("closure did not join the active operation")
		default:
		}
		work.Run("late", func() { runs.Add(100) })
		close(release)
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("closure did not finish after the operation")
		}
		assert.Equal(t, int64(1), runs.Load(), "closure suppresses pending repeats and new keys")
		assert.True(t, work.Idle())
		work.CloseAndWait()
		work.Run("after-close", func() { runs.Add(1000) })
		synctest.Wait()
		assert.Equal(t, int64(1), runs.Load())
	})
}
