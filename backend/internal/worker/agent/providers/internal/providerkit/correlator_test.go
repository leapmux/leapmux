package providerkit

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorrelatorObservesBeforeDeliveryAndClaimsTheRequestOnce(t *testing.T) {
	t.Parallel()
	var correlator Correlator[string]
	var channel <-chan json.RawMessage
	var observed atomic.Int32
	channel, release := correlator.RegisterObserved("native-request", func(raw json.RawMessage) {
		observed.Add(1)
		assert.Equal(t, `{"exact":0}`, string(raw))
		assert.False(t, correlator.IsPendingForTest("native-request"))
		select {
		case <-channel:
			t.Error("delivery preceded its observer")
		default:
		}
	})
	defer release()
	require.True(t, correlator.Deliver("native-request", json.RawMessage(`{"exact":0}`)))
	assert.Equal(t, int32(1), observed.Load())
	assert.Equal(t, `{"exact":0}`, string(<-channel))
	assert.False(t, correlator.Deliver("native-request", json.RawMessage(`{"duplicate":true}`)))
	assert.Equal(t, int32(1), observed.Load())
}

func TestCorrelatorDoesNotObserveAReleasedRequest(t *testing.T) {
	t.Parallel()
	var correlator Correlator[int64]
	var observed atomic.Int32
	_, release := correlator.RegisterObserved(1, func(json.RawMessage) { observed.Add(1) })
	release()
	assert.False(t, correlator.Deliver(1, json.RawMessage(`null`)))
	assert.Zero(t, observed.Load())
}

func TestConcurrentCorrelatorRepliesCallOneObserver(t *testing.T) {
	t.Parallel()
	var correlator Correlator[int64]
	var observed, delivered atomic.Int32
	channel, release := correlator.RegisterObserved(1, func(json.RawMessage) { observed.Add(1) })
	defer release()
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if correlator.Deliver(1, json.RawMessage(`false`)) {
				delivered.Add(1)
			}
		})
	}
	group.Wait()
	assert.Equal(t, int32(1), observed.Load())
	assert.Equal(t, int32(1), delivered.Load())
	assert.Equal(t, "false", string(<-channel))
}
