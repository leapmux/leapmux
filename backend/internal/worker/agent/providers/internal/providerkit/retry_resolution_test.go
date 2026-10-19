package providerkit

import (
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryResolutionWritesOnceAfterARetry(t *testing.T) {
	t.Parallel()
	var retry RetryResolution
	sink := &agenttest.Sink{}
	retry.NoteRetry()
	retry.NoteRetry()
	retry.Resolve(sink)
	retry.Resolve(sink)
	assert.Equal(t, []map[string]interface{}{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
}

func TestRetryResolutionWritesNothingWithoutARetry(t *testing.T) {
	t.Parallel()
	var retry RetryResolution
	sink := &agenttest.Sink{}
	retry.Resolve(sink)
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestRetryResolutionResetForgetsTheRetry(t *testing.T) {
	t.Parallel()
	var retry RetryResolution
	sink := &agenttest.Sink{}
	retry.NoteRetry()
	retry.Reset()
	retry.Resolve(sink)
	assert.Empty(t, sink.LeapMuxNotifications())
	retry.NoteRetry()
	retry.Resolve(sink)
	assert.Len(t, sink.LeapMuxNotifications(), 1)
}

func TestRetryResolutionWritesOnceAcrossConcurrentResolves(t *testing.T) {
	t.Parallel()
	var retry RetryResolution
	sink := &agenttest.Sink{}
	retry.NoteRetry()
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() { retry.Resolve(sink) })
	}
	group.Wait()
	assert.Len(t, sink.LeapMuxNotifications(), 1)
}

type reentrantRetrySink struct {
	agenttest.Sink
	retry *RetryResolution
}

func (s *reentrantRetrySink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.retry.NoteRetry()
	s.Sink.PersistLeapMuxNotification(content)
}

func TestRetryResolutionCallsTheSinkOutsideItsLock(t *testing.T) {
	t.Parallel()
	var retry RetryResolution
	sink := &reentrantRetrySink{retry: &retry}
	retry.NoteRetry()
	retry.Resolve(sink)
	retry.Resolve(&sink.Sink)
	require.Len(t, sink.LeapMuxNotifications(), 2)
}
