// Package clockjump reports the periods in which the machine's clock stopped.
//
// Go's monotonic clock STOPS while the machine is asleep (darwin's
// mach_absolute_time, Linux's CLOCK_MONOTONIC), and every time.Timer and
// time.Ticker built on it stops with it. The wall clock does not stop. So a
// laptop that sleeps for an hour wakes into a process whose timers all believe
// no time passed, holding deadlines that expired long ago, sockets the peer
// closed, and tokens the server already rejected.
//
// Each component discovers that separately and reports it as its own symptom --
// a lease that "was removed, replaced, or expired", a stream that ended, an
// authentication that failed. None of them can name the cause, because the cause
// is a process-wide fact that no single component can observe. This package
// observes it once and states it plainly, so every symptom in the same window
// has an explanation instead of a guess.
//
// It detects the clock stopping, NOT the process stopping. SIGSTOP and a loaded
// scheduler leave the monotonic clock running, so neither is reported; see skew.
//
// It only reports. Correctness after a pause belongs to the component that owns
// the state: the revocation watcher, for one, compares BOTH clock readings on
// its own lease rather than waiting for this detector's next sample, because a
// sampled report arrives too late to gate a write.
package clockjump

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// defaultInterval is how often the detector compares the two clocks. It also
	// bounds how late a report is: a pause is reported on the first tick after
	// the process resumes, because the ticker was frozen for the pause too.
	defaultInterval = 10 * time.Second
	// defaultThreshold is the smallest skew worth a log line. The monotonic clock
	// keeps running while this process is descheduled, so load and garbage
	// collection move the two clocks apart by microseconds, not seconds; an NTP
	// slew is capped near 500 parts per million, which is 5ms over a 10s
	// interval. Seconds of skew therefore means the machine's clock genuinely
	// stopped or genuinely stepped.
	defaultThreshold = 2 * time.Second
)

// jump is how far the two clocks moved apart across one sample interval.
type jump struct {
	// wall is how much the wall clock advanced.
	wall time.Duration
	// monotonic is how much the monotonic clock advanced over the same interval,
	// which is how much of it this process was running for.
	monotonic time.Duration
}

// skew is how much MORE the wall clock advanced than the monotonic one. It is
// positive when the machine's clock stopped (system sleep, or a hypervisor that
// paused the guest) and negative when the wall clock stepped backwards (a clock
// correction).
//
// A merely STOPPED process produces no skew. SIGSTOP and a machine too loaded to
// schedule this process both leave mach_absolute_time and CLOCK_MONOTONIC
// running, because they count time since boot rather than time on the CPU. That
// is a feature here: it is what keeps the detector silent under load instead of
// reporting a pause every time the scheduler is busy.
func (j jump) skew() time.Duration { return j.wall - j.monotonic }

// clock supplies the two readings the detector compares.
//
// It is an interface ONLY because the divergence cannot be constructed. No Go
// API builds a time.Time whose wall and monotonic readings disagree: time.Now
// is the only source of a monotonic reading, Add moves both by the same amount,
// and AddDate, Round, Truncate and UTC all strip the monotonic one instead. A
// test can therefore only inject the divergence, which needs the two readings to
// come from separate calls.
type clock interface {
	monotonic() time.Duration
	wall() time.Time
}

// systemClock reads both clocks from the operating system. monotonic counts from
// a time.Now captured at construction, so time.Since uses that instant's
// monotonic reading and advances only while the process runs. wall strips the
// monotonic reading with Round(0), so subtraction between two of them is a true
// wall-clock difference.
type systemClock struct{ startedAt time.Time }

func newSystemClock() systemClock { return systemClock{startedAt: time.Now()} }

func (c systemClock) monotonic() time.Duration { return time.Since(c.startedAt) }
func (systemClock) wall() time.Time            { return time.Now().Round(0) }

// detector compares the two clocks on an interval and reports every skew past
// its threshold. It is not safe for concurrent use; one loop owns one detector.
type detector struct {
	clock     clock
	interval  time.Duration
	threshold time.Duration

	lastMonotonic time.Duration
	lastWall      time.Time
}

func newDetector(c clock, interval, threshold time.Duration) *detector {
	return &detector{
		clock:         c,
		interval:      interval,
		threshold:     threshold,
		lastMonotonic: c.monotonic(),
		lastWall:      c.wall(),
	}
}

// sample compares both clocks against the previous sample and reports whether
// they moved apart by at least the threshold. It advances the baseline on EVERY
// call, reported or not, so one pause is reported once rather than counted again
// into each later sample.
func (d *detector) sample() (jump, bool) {
	monotonic, wall := d.clock.monotonic(), d.clock.wall()
	observed := jump{
		wall:      wall.Sub(d.lastWall),
		monotonic: monotonic - d.lastMonotonic,
	}
	d.lastMonotonic, d.lastWall = monotonic, wall

	skew := observed.skew()
	if skew < 0 {
		skew = -skew
	}
	if skew < d.threshold {
		return jump{}, false
	}
	return observed, true
}

// run samples until ctx ends.
func (d *detector) run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if observed, ok := d.sample(); ok {
				report(observed)
			}
		}
	}
}

// report states the pause in the terms the reader needs: how long it lasted, and
// that every timer and deadline in the process stopped with it. WARN rather than
// INFO because its whole purpose is to explain the warnings and errors that
// surround it.
func report(observed jump) {
	if observed.skew() < 0 {
		slog.Warn("the wall clock stepped backwards; deadlines measured against it now expire later than intended",
			"stepped_back", -observed.skew(),
			"wall_elapsed", observed.wall,
			"monotonic_elapsed", observed.monotonic)
		return
	}
	slog.Warn("this process did not run for a period; every timer and deadline stopped with it",
		"paused_for", observed.skew(),
		"wall_elapsed", observed.wall,
		"monotonic_elapsed", observed.monotonic)
}

// running guards the process-wide loop. A process has one clock, so it needs one
// detector; a solo process runs a Hub and a Worker that would otherwise each
// start one and report every pause twice.
var running struct {
	mu sync.Mutex
	on bool
}

// StartLoop starts the process-wide detector and stops it when ctx ends.
//
// Call it from every component that wants the guarantee. While a loop is
// running, later calls are no-ops that neither extend nor shorten its life, so
// the FIRST caller's context governs -- acceptable because every caller's
// context ends at process shutdown. Once that context ends the guard clears, so
// a process that tears its components down and builds them again (the desktop
// sidecar switching between solo and remote mode) gets a detector back.
func StartLoop(ctx context.Context) {
	running.mu.Lock()
	defer running.mu.Unlock()
	if running.on {
		return
	}
	running.on = true

	d := newDetector(newSystemClock(), defaultInterval, defaultThreshold)
	go func() {
		defer func() {
			running.mu.Lock()
			running.on = false
			running.mu.Unlock()
		}()
		d.run(ctx)
	}()
}
