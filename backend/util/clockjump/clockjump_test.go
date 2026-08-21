package clockjump

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// fakeClock drives the two readings apart, which is the whole point of the
// clock seam: a suspend advances the wall clock while the monotonic clock stays
// put, and no time.Time can be built that way (see the clock interface).
type fakeClock struct {
	mono time.Duration
	now  time.Time
}

func newFakeClock() *fakeClock {
	// A fixed instant, not time.Now: the wall reading must be free of a
	// monotonic reading so Sub between two of them is a true wall difference.
	return &fakeClock{now: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) monotonic() time.Duration { return c.mono }
func (c *fakeClock) wall() time.Time          { return c.now }

// advance moves both clocks by the same amount: an ordinary interval in which
// the process ran the whole time.
func (c *fakeClock) advance(d time.Duration) {
	c.mono += d
	c.now = c.now.Add(d)
}

// pause moves ONLY the wall clock, which is what a suspended process observes:
// real time passed and it ran for none of it.
func (c *fakeClock) pause(d time.Duration) { c.now = c.now.Add(d) }

// stepBack moves ONLY the wall clock backwards, as a clock correction does.
func (c *fakeClock) stepBack(d time.Duration) { c.now = c.now.Add(-d) }

func TestSampleIsQuietWhileTheProcessKeepsRunning(t *testing.T) {
	c := newFakeClock()
	d := newDetector(c, defaultInterval, defaultThreshold)

	for range 5 {
		c.advance(defaultInterval)
		_, ok := d.sample()
		assert.False(t, ok, "both clocks advanced together; there is nothing to report")
	}
}

func TestSampleIgnoresSkewUnderTheThreshold(t *testing.T) {
	c := newFakeClock()
	d := newDetector(c, defaultInterval, defaultThreshold)

	// Scheduling jitter and garbage collection produce exactly this shape, and
	// a log line for every one of them would bury the pauses that matter.
	c.advance(defaultInterval)
	c.pause(defaultThreshold - time.Millisecond)
	_, ok := d.sample()
	assert.False(t, ok, "sub-threshold skew must not be reported")
}

func TestSampleReportsAPauseOnce(t *testing.T) {
	c := newFakeClock()
	d := newDetector(c, defaultInterval, defaultThreshold)

	// The ticker is frozen for the pause too, so the interval that spans a
	// 47-minute sleep advances the monotonic clock by one ordinary tick.
	c.advance(defaultInterval)
	c.pause(47 * time.Minute)
	observed, ok := d.sample()
	require.True(t, ok)
	assert.Equal(t, 47*time.Minute, observed.skew(), "the skew is how long the process did not run")
	assert.Equal(t, 47*time.Minute+defaultInterval, observed.wall)
	assert.Equal(t, defaultInterval, observed.monotonic)

	// The baseline advanced with the reported sample, so the SAME pause is not
	// counted again into every later interval.
	c.advance(defaultInterval)
	_, ok = d.sample()
	assert.False(t, ok, "a pause must be reported once, not on every sample after it")
}

func TestSampleReportsAWallClockStepBackwards(t *testing.T) {
	c := newFakeClock()
	d := newDetector(c, defaultInterval, defaultThreshold)

	c.advance(defaultInterval)
	c.stepBack(30 * time.Second)
	observed, ok := d.sample()
	require.True(t, ok)
	assert.Equal(t, -30*time.Second, observed.skew(),
		"a backwards step is a negative skew, so the report can tell it from a pause")
}

func TestReportNamesThePauseAndItsConsequence(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)

	report(jump{wall: 47*time.Minute + 10*time.Second, monotonic: 10 * time.Second})

	out := logs.String()
	assert.Contains(t, out, "this process did not run for a period")
	assert.Contains(t, out, "every timer and deadline stopped with it",
		"the consequence is what lets a reader explain the errors around this line")
	assert.Contains(t, out, "paused_for=47m0s")
	assert.Contains(t, out, "level=WARN", "the line exists to explain nearby warnings; INFO would be filtered with the noise")
}

func TestReportDistinguishesABackwardsStepFromAPause(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)

	report(jump{wall: 0, monotonic: 30 * time.Second})

	out := logs.String()
	assert.Contains(t, out, "the wall clock stepped backwards")
	assert.Contains(t, out, "stepped_back=30s")
	assert.NotContains(t, out, "did not run", "a clock correction is not a pause and must not be reported as one")
}

// The loop must survive an ordinary interval and report a pause through it, so
// the wiring between the ticker, sample, and report is covered and not just the
// pieces.
func TestRunReportsAPauseObservedByTheLoop(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	c := newFakeClock()
	// A tiny interval so the loop ticks promptly; the threshold stays large so
	// only the injected pause can trip it, never the real time this test takes.
	d := newDetector(c, time.Millisecond, defaultThreshold)
	c.pause(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); d.run(ctx) }()

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "paused_for=1h0m0s")
	}, 2*time.Second, time.Millisecond, "the loop must report the pause its sample observed")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return when its context ended")
	}
}

// A process has ONE clock, so a solo process that runs a Hub and a Worker must
// not report every pause twice.
//
// Each caller gets its OWN context, which is what makes this more than a
// restatement of the guard: `running.on` would read true even if all three calls
// had started a loop. Cancelling the later callers' contexts must not stop
// anything, because their calls were no-ops -- and then cancelling the FIRST
// caller's context must stop the one loop that does exist.
func TestStartLoopRunsOneDetectorPerProcess(t *testing.T) {
	first, cancelFirst := context.WithCancel(context.Background())
	t.Cleanup(func() { cancelFirst(); waitForLoopToStop() })
	second, cancelSecond := context.WithCancel(context.Background())
	third, cancelThird := context.WithCancel(context.Background())

	StartLoop(first)
	require.True(t, loopIsRunning(), "the first call must start a loop")
	StartLoop(second) // a second component asking for the same guarantee
	StartLoop(third)

	cancelSecond()
	cancelThird()
	// Long enough that a loop owned by either context would have exited.
	time.Sleep(20 * time.Millisecond)
	assert.True(t, loopIsRunning(),
		"the later calls started no loop of their own, so their contexts control nothing")

	cancelFirst()
	waitForLoopToStop()
	assert.False(t, loopIsRunning(),
		"the first caller's context governs the only loop, so ending it ends the detector")
}

// The desktop sidecar tears its components down and builds them again when it
// switches between solo and remote mode. A guard that latched forever would
// leave that process without a detector for the rest of its life.
func TestStartLoopStartsAgainAfterItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	StartLoop(ctx)
	require.True(t, loopIsRunning())

	cancel()
	waitForLoopToStop()

	restarted, cancelRestarted := context.WithCancel(context.Background())
	defer cancelRestarted()
	StartLoop(restarted)
	t.Cleanup(waitForLoopToStop)
	assert.True(t, loopIsRunning(), "a torn-down process must get a detector back")
}

func loopIsRunning() bool {
	running.mu.Lock()
	defer running.mu.Unlock()
	return running.on
}

// waitForLoopToStop blocks until the package-global guard clears. The guard is
// process-wide, so a test that returned while its loop was still unwinding would
// make the NEXT test's StartLoop a no-op and fail it for no reason of its own.
func waitForLoopToStop() {
	for range 2000 {
		if !loopIsRunning() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
