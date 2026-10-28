package droid

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestIDsRemainIndependentAndUniqueAcrossConcurrentCallers(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.startTurn()
	const callers, requestsPerCaller = 16, 8
	ids := make(chan string, callers*requestsPerCaller)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range callers {
		workers.Go(func() {
			<-start
			for range requestsPerCaller {
				ids <- a.nextRequestID()
			}
		})
	}
	close(start)
	workers.Wait()
	close(ids)
	unique := make(map[string]bool, callers*requestsPerCaller)
	for id := range ids {
		assert.False(t, unique[id], "concurrent native requests must not share an inbox identity: %s", id)
		unique[id] = true
	}
	require.Len(t, unique, callers*requestsPerCaller)
	for number := 1; number <= callers*requestsPerCaller; number++ {
		assert.Contains(t, unique, fmt.Sprintf("leapmux-%d", number), "turn publication must not consume a native request identity")
	}
	assert.True(t, a.PublishTurnActive().Active)
	assert.Equal(t, []uint64{1, 2}, sink.TurnSeqs(), "native requests must not consume turn ordering tokens")
}
