package testutil

import "sync/atomic"

// TrackPeak records one entry into a concurrent section and returns the exit.
//
// It answers the one question a concurrency-limit test asks: how many callers
// were inside the section at the same time. inFlight is the live count and peak
// is the high-water mark, both owned by the caller so a test can read either.
//
// Use it as the first line of the section:
//
//	defer TrackPeak(&f.inFlight, &f.peak)()
//
// The peak update is a compare-and-swap loop rather than a plain store, because
// two callers that enter together otherwise race and the LATER one can store the
// lower count -- which reports a peak below the overlap that actually happened,
// and that is the direction a limit test must never be wrong in.
func TrackPeak(inFlight, peak *atomic.Int32) func() {
	now := inFlight.Add(1)
	for {
		seen := peak.Load()
		if now <= seen || peak.CompareAndSwap(seen, now) {
			break
		}
	}
	return func() { inFlight.Add(-1) }
}
