package main

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDrainDoesNotLeakWaiterOnAbandonedStraggler is the operationGate side of
// the #297 reproducer (rpc_test.go's
// TestDrainHandlersDoesNotLeakWaiterOnAbandonedStraggler covers the
// drainHandlers side the leak was reported on): a permanently-stuck operation
// (released only in Cleanup) must not leave a waiter goroutine parked per
// timed-out drain. Pre-fix waitGroupDone spawned one such waiter per drain;
// post-fix drain.Counter removes the spawn entirely.
//
// The scan keys on sync.(*WaitGroup).Wait / util/drain frames -- not the
// waitGroupDone symbol -- so the assertion stays meaningful after the fix
// deletes that helper.
func TestDrainDoesNotLeakWaiterOnAbandonedStraggler(t *testing.T) {
	var g operationGate
	done, ok := g.begin()
	require.True(t, ok)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		done()
	})
	// Keep a live reference to release so the stuck admission is "an operation"
	// that never returns until Cleanup -- mirroring a non-cancellable exec.
	go func() { <-release }()

	const drains = 8
	for range drains {
		g.drain(time.Millisecond, "")
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastDump string
	for {
		lastDump = allGoroutineStacks()
		leaked := countDrainWaiterFrames(lastDump)
		if leaked == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected no drain waiter goroutines after abandoning a straggler %d times; found %d\n%s",
				drains, leaked, lastDump)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func allGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// countDrainWaiterFrames counts goroutines whose stacks show a leaked drain
// waiter: parked in sync.(*WaitGroup).Wait (the shape the pre-fix waitGroupDone
// bridge leaked), or carrying a util/drain frame (insurance against a
// reintroduced waiter of any other shape -- nothing legitimately runs drain
// code once every drain has returned, and the leak tests scan only then, with
// no concurrent test executing a drain). The stuck operation itself waits on a
// channel and does not match.
func countDrainWaiterFrames(dump string) int {
	count := 0
	for _, block := range strings.Split(dump, "\n\n") {
		if strings.Contains(block, "sync.(*WaitGroup).Wait") ||
			strings.Contains(block, "util/drain") {
			count++
		}
	}
	return count
}
