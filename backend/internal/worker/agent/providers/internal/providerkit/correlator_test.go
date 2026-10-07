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
	channel, release, err := correlator.RegisterObserved("native-request", func(raw json.RawMessage) {
		observed.Add(1)
		assert.Equal(t, `{"exact":0}`, string(raw))
		assert.False(t, correlator.IsPendingForTest("native-request"))
		select {
		case <-channel:
			t.Error("delivery preceded its observer")
		default:
		}
	})
	require.NoError(t, err)
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
	_, release, err := correlator.RegisterObserved(1, func(json.RawMessage) { observed.Add(1) })
	require.NoError(t, err)
	release()
	assert.False(t, correlator.Deliver(1, json.RawMessage(`null`)))
	assert.Zero(t, observed.Load())
}

func TestConcurrentCorrelatorRepliesCallOneObserver(t *testing.T) {
	t.Parallel()
	var correlator Correlator[int64]
	var observed, delivered atomic.Int32
	channel, release, err := correlator.RegisterObserved(1, func(json.RawMessage) { observed.Add(1) })
	require.NoError(t, err)
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

func TestCorrelatorReleaseKeepsAReplacementRegistration(t *testing.T) {
	t.Parallel()
	var correlator Correlator[int64]
	first, releaseFirst, err := correlator.Register(1)
	require.NoError(t, err)
	require.True(t, correlator.Deliver(1, json.RawMessage(`"first"`)))
	assert.Equal(t, `"first"`, string(<-first))
	second, releaseSecond, err := correlator.Register(1)
	require.NoError(t, err)
	defer releaseSecond()
	releaseFirst()
	require.True(t, correlator.Deliver(1, json.RawMessage(`"second"`)), "old cleanup must not remove a replacement registration")
	assert.Equal(t, `"second"`, string(<-second))
}

func TestCorrelatorRefusesConcurrentRegistrationsForOneID(t *testing.T) {
	t.Parallel()
	var correlator Correlator[int64]
	type registration struct {
		channel <-chan json.RawMessage
		release func()
		err     error
	}
	registrations := make(chan registration, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			channel, release, err := correlator.Register(0)
			registrations <- registration{channel, release, err}
		})
	}
	group.Wait()
	close(registrations)
	var winner registration
	accepted := 0
	for candidate := range registrations {
		if candidate.err == nil {
			accepted++
			winner = candidate
		} else {
			assert.Nil(t, candidate.channel)
			assert.Nil(t, candidate.release)
		}
	}
	require.Equal(t, 1, accepted)
	defer winner.release()
	require.True(t, correlator.Deliver(0, json.RawMessage(`0`)))
	assert.Equal(t, "0", string(<-winner.channel))
}
