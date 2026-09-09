package service

import (
	"sync"
	"testing"
	"time"

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
	})
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

	publisher := newGenerationProgressPublisher(func(map[string]interface{}) {})
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
	})
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
