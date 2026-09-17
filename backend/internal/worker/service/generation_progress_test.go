package service

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationProgressPublisherCoalescesGrowthAndClearsImmediately(t *testing.T) {
	t.Parallel()

	updates := make(chan map[string]interface{}, 2)
	publisher := newGenerationProgressPublisher(func(info map[string]interface{}) {
		updates <- info
	}, func() string { return "session" })
	t.Cleanup(publisher.close)

	publisher.report(agent.ModelTextProgress("model", "abcdefgh"))
	publisher.report(agent.ModelTextProgress("model", "ijklmnop"))

	select {
	case info := <-updates:
		assert.Equal(t, int64(4), info[contracts.SessionInfoKeyThinkingTokens])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the coalesced progress update")
	}

	publisher.report(agent.CompleteModelProgress("model"))
	select {
	case info := <-updates:
		assert.Equal(t, int64(0), info[contracts.SessionInfoKeyThinkingTokens])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the immediate progress clear")
	}
}

func TestGenerationProgressPublisherReplaysOnlyActiveValues(t *testing.T) {
	t.Parallel()

	publisher := newGenerationProgressPublisher(func(map[string]interface{}) {}, func() string { return "session" })
	t.Cleanup(publisher.close)
	publisher.report(agent.OutputTotalProgress("tool", 2048, true))

	info := publisher.snapshotInfo()
	require.NotNil(t, info)
	assert.Equal(t, int64(2048), info[contracts.SessionInfoKeyOutputBytes])
	assert.Equal(t, true, info[contracts.SessionInfoKeyOutputBytesMinimum])
}

func TestGenerationProgressPublisherSerializesAResetAfterAPendingSend(t *testing.T) {
	t.Parallel()

	positiveStarted := make(chan struct{})
	releasePositive := make(chan struct{})
	zeroStarted := make(chan struct{})
	var positiveOnce sync.Once
	var zeroOnce sync.Once
	updates := make(chan int64, 2)
	publisher := newGenerationProgressPublisher(func(info map[string]interface{}) {
		value := info[contracts.SessionInfoKeyThinkingTokens].(int64)
		if value > 0 {
			positiveOnce.Do(func() { close(positiveStarted) })
			<-releasePositive
		} else {
			zeroOnce.Do(func() { close(zeroStarted) })
		}
		updates <- value
	}, func() string { return "session" })
	t.Cleanup(publisher.close)

	publisher.report(agent.ModelTextProgress("model", "abcdefgh"))
	select {
	case <-positiveStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the positive send")
	}

	resetDone := make(chan struct{})
	go func() {
		publisher.report(agent.ResetProgress())
		close(resetDone)
	}()
	select {
	case <-zeroStarted:
		t.Fatal("the reset passed a pending positive send")
	case <-time.After(50 * time.Millisecond):
	}
	close(releasePositive)

	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the reset")
	}
	assert.Equal(t, int64(2), <-updates)
	assert.Equal(t, int64(0), <-updates)
}

func TestGenerationProgressPublisherIgnoresReportsAfterClose(t *testing.T) {
	t.Parallel()

	publisher := newGenerationProgressPublisher(func(map[string]interface{}) {
		t.Fatal("a closed publisher must not send")
	}, func() string { return "session" })
	publisher.close()
	publisher.report(agent.NativeTokenProgress("late-model", 42))

	publisher.mu.Lock()
	snapshot := publisher.counter.Snapshot()
	publisher.mu.Unlock()
	assert.Equal(t, agent.ProgressSnapshot{}, snapshot)
}

// The live output of a RUNNING tool reaches the browser on the span-keyed
// running_tool payload, throttled and capped. It is never a counter: two calls
// print at once, and each tail belongs to the one call that wrote it.
func TestGenerationProgressPublisherBroadcastsOneOutputTailPerSpan(t *testing.T) {
	t.Parallel()

	updates := make(chan map[string]interface{}, 4)
	publisher := newGenerationProgressPublisher(func(info map[string]interface{}) {
		updates <- info
	}, func() string { return "session-1" })
	publisher.tailInterval = time.Millisecond
	t.Cleanup(publisher.close)

	publisher.report(agent.OutputTailProgress("call-a", "first", false))
	publisher.report(agent.OutputTailProgress("call-a", "first\nsecond", false))
	publisher.report(agent.OutputTailProgress("call-b", "other", true))

	tails := map[string]map[string]interface{}{}
	for len(tails) < 2 {
		select {
		case info := <-updates:
			running, ok := info[contracts.SessionInfoKeyRunningTool].(map[string]interface{})
			require.True(t, ok, "the tail rides the running_tool key")
			tails[running[contracts.RunningToolFieldSpanId].(string)] = running
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for the tails; saw %d", len(tails))
		}
	}

	// The LATEST text for the span, not the first one it reported: the worker
	// coalesces inside the window, and the provider sends what it wants shown.
	assert.Equal(t, "first\nsecond", tails["call-a"][contracts.RunningToolFieldOutputTail])
	assert.Equal(t, false, tails["call-a"][contracts.RunningToolFieldOutputTruncated])
	assert.Equal(t, "session-1", tails["call-a"][contracts.RunningToolFieldAgentSessionId])
	assert.Equal(t, "other", tails["call-b"][contracts.RunningToolFieldOutputTail])
	assert.Equal(t, true, tails["call-b"][contracts.RunningToolFieldOutputTruncated])
}

// A tail must not revive an entry the browser dropped when the result landed.
//
// It asserts the STATE the completion leaves rather than the absence of a
// broadcast: a test that waited for a window to pass would pass for as long as
// the window outlives the assertion, whatever the code did.
func TestGenerationProgressPublisherForgetsATailWhenItsCallEnds(t *testing.T) {
	t.Parallel()

	publisher := newGenerationProgressPublisher(func(map[string]interface{}) {}, func() string { return "session-1" })
	t.Cleanup(publisher.close)

	publisher.report(agent.OutputTailProgress("call-a", "partial", false))
	publisher.report(agent.OutputTailProgress("call-b", "other", false))
	publisher.mu.Lock()
	require.Len(t, publisher.tails, 2)
	publisher.mu.Unlock()

	// The completion of one call leaves the other call's tail alone.
	publisher.report(agent.CompleteOutputProgress("call-a"))
	publisher.mu.Lock()
	_, stillThere := publisher.tails["call-a"]
	remaining := len(publisher.tails)
	publisher.mu.Unlock()
	assert.False(t, stillThere, "a finished call keeps no tail")
	assert.Equal(t, 1, remaining)

	// A whole-agent reset forgets every one of them.
	publisher.report(agent.ResetProgress())
	publisher.mu.Lock()
	empty := len(publisher.tails)
	publisher.mu.Unlock()
	assert.Equal(t, 0, empty)
}

// The operations that do NOT end a tool call keep its tail. A model operation is
// the case worth pinning: the two counters are independent, so a turn that finished
// thinking can still run the command whose output the reader watches.
func TestGenerationProgressPublisherKeepsATailAcrossEveryOperationThatDoesNotEndTheCall(t *testing.T) {
	t.Parallel()

	for _, update := range []agent.ProgressUpdate{
		agent.ModelTextProgress("call-a", "thinking"),
		agent.OutputDeltaProgress("call-a", 12),
		agent.OutputTotalProgress("call-a", 40, false),
		agent.CompleteModelProgress("call-a"),
		agent.ResetModelProgress(),
		// A tail for a DIFFERENT call ends nothing at all.
		agent.OutputTailProgress("call-b", "other", false),
	} {
		publisher := newGenerationProgressPublisher(func(map[string]interface{}) {}, func() string { return "session-1" })
		publisher.report(agent.OutputTailProgress("call-a", "partial", false))
		publisher.report(update)
		publisher.mu.Lock()
		entry, present := publisher.tails["call-a"]
		publisher.mu.Unlock()
		require.True(t, present, "operation %v must keep the tail of a call it did not end", update.Operation)
		assert.Equal(t, "partial", entry.text)
		publisher.close()
	}
}

// The cap keeps the LAST bytes, because that is what a reader watches, and it
// cuts at a rune boundary so no replacement character reaches the browser.
func TestClipOutputTailKeepsTheEndAtARuneBoundary(t *testing.T) {
	t.Parallel()

	short := "already short"
	text, clipped := clipOutputTail(short)
	assert.Equal(t, short, text)
	assert.False(t, clipped)

	long := strings.Repeat("가", runningToolTailBytes) // three bytes each
	text, clipped = clipOutputTail(long)
	assert.True(t, clipped)
	assert.LessOrEqual(t, len(text), runningToolTailBytes)
	assert.True(t, utf8.ValidString(text), "the cut must land on a rune boundary")
	assert.True(t, strings.HasSuffix(long, text), "the cap keeps the end, never the start")
}
