package testutil

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrackPeak_RecordsTheHighWaterMark(t *testing.T) {
	t.Parallel()

	var inFlight, peak atomic.Int32
	const overlap = 8

	// Hold every caller inside the section at once, so the peak must reach the
	// full overlap and cannot be satisfied by a serialized run.
	var start, done sync.WaitGroup
	release := make(chan struct{})
	start.Add(overlap)
	done.Add(overlap)
	for range overlap {
		go func() {
			defer done.Done()
			exit := TrackPeak(&inFlight, &peak)
			start.Done()
			<-release
			exit()
		}()
	}
	start.Wait()
	assert.Equal(t, int32(overlap), inFlight.Load(), "every caller must be inside the section")
	close(release)
	done.Wait()

	assert.Equal(t, int32(overlap), peak.Load(), "the peak must record the full overlap")
	assert.Zero(t, inFlight.Load(), "every exit must decrement the live count")
}

func TestTrackPeak_APeakNeverFalls(t *testing.T) {
	t.Parallel()

	var inFlight, peak atomic.Int32
	TrackPeak(&inFlight, &peak)()
	TrackPeak(&inFlight, &peak)()
	assert.Equal(t, int32(1), peak.Load(),
		"a later, lower entry must not lower the mark an earlier overlap set")
}
