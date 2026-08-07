import { describe, expect, it } from 'vitest'
import { createScrollVelocity } from './chatScrollVelocity'

describe('createScrollVelocity', () => {
  it('measures the sample after a programmatic write over the interval since the WRITE, not the last real sample', () => {
    let clock = 0
    const v = createScrollVelocity({ now: () => clock, thresholdPxPerMs: 1, idleMs: 100 })
    // Seed with a real sample.
    v.sample(0)
    // A long gap, then a programmatic write (a re-pin) jumps the position.
    clock = 200
    v.syncToProgrammatic(1000)
    // The user flicks: 100px in 10ms = 10 px/ms, well above the 1 px/ms threshold.
    clock = 210
    v.sample(1100)
    // dt is measured from the write (200), not the seed (0): velocity ~10, a fling.
    // The pre-fix code measured from 0 (dt=210 -> ~0.48 px/ms) and misclassified it as
    // a slow scroll, cancelling the real fling's momentum.
    expect(v.isActivelyFlinging()).toBe(true)
    expect(v.speed()).toBeCloseTo(10, 5)
  })

  it('still reports idle from the last REAL sample, ignoring a programmatic write', () => {
    let clock = 0
    const v = createScrollVelocity({ now: () => clock, thresholdPxPerMs: 1, idleMs: 100 })
    v.sample(0)
    clock = 10
    v.sample(50) // a real fling sample: 50px / 10ms = 5 px/ms
    expect(v.isActivelyFlinging()).toBe(true)
    // A programmatic write much later must NOT reset the idle clock -- momentum has
    // long since stopped, so a write is safe (isActivelyFlinging false, no overscan).
    clock = 500
    v.syncToProgrammatic(9999)
    expect(v.isActivelyFlinging()).toBe(false) // now - lastTime (500-10) > idleMs
    expect(v.speed()).toBe(0)
  })

  it('drops a same-tick coalesced sample without re-baselining (keeps the prior interval)', () => {
    let clock = 0
    const v = createScrollVelocity({ now: () => clock, thresholdPxPerMs: 1, idleMs: 100 })
    v.sample(0)
    clock = 10
    v.sample(50) // velocity 5 px/ms
    // A same-tick coalesced event carries no measurable interval; it is dropped and
    // the velocity is unchanged.
    v.sample(80)
    expect(v.speed()).toBeCloseTo(5, 5)
  })

  it('attributes a coalesced jump to the NEXT interval from the last TIMED baseline', () => {
    let clock = 0
    const v = createScrollVelocity({ now: () => clock, thresholdPxPerMs: 1, idleMs: 100 })
    v.sample(0)
    clock = 10
    v.sample(100) // timed baseline: pos 100 @ t=10
    // A same-tick event jumps far (100 -> 5000) but carries no measurable interval,
    // so it is dropped WITHOUT moving the baseline (lastPos stays 100, not 5000).
    v.sample(5000)
    // The next real sample 1ms later measures the average over the whole span from
    // the last TIMED baseline: |5100 - 100| / (11 - 10) = 5000 px/ms. Were the
    // dropped event to (wrongly) re-baseline lastPos to 5000, this would read
    // |5100 - 5000| / 1 = 100 px/ms instead -- under-measuring the real motion.
    clock = 11
    v.sample(5100)
    expect(v.speed()).toBeCloseTo(5000, 5)
  })

  describe('isCoasting', () => {
    const make = (now: () => number) => createScrollVelocity({ now, thresholdPxPerMs: 1, idleMs: 100 })

    it('stays latched through the decaying tail a speed test calls stopped', () => {
      // The whole reason this predicate exists. Momentum decays continuously, so the coast
      // crosses below the fling threshold while the viewport still plainly moves. A scrollTop
      // write in that window kills the coast under the reader's finger.
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      clock = 10
      v.sample(100) // 10 px/ms: a fling, which latches the coast
      expect(v.isCoasting()).toBe(true)
      expect(v.isActivelyFlinging()).toBe(true)

      clock = 20
      v.sample(105) // 0.5 px/ms: under the threshold, but still 5px of real motion
      expect(v.isActivelyFlinging()).toBe(false) // the speed test gives up here
      expect(v.isCoasting()).toBe(true) // ...and this is why it must not decide

      clock = 30
      v.sample(107) // 0.2 px/ms: slower still, and still moving
      expect(v.isCoasting()).toBe(true)
    })

    it('clears once the motion actually stops', () => {
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      clock = 10
      v.sample(100)
      expect(v.isCoasting()).toBe(true)
      clock = 20
      v.sample(100) // the viewport came to rest: zero displacement
      expect(v.isCoasting()).toBe(false)
    })

    it('clears when the samples go stale, so a resumed gesture starts clean', () => {
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      clock = 10
      v.sample(100)
      expect(v.isCoasting()).toBe(true)
      clock = 200 // past idleMs with no sample: the motion ended
      expect(v.isCoasting()).toBe(false)
      // A slow gesture that resumes after the gap must not inherit the old latch.
      v.sample(150)
      clock = 400
      v.sample(160) // 0.05 px/ms
      expect(v.isCoasting()).toBe(false)
    })

    it('never latches on a slow deliberate scroll', () => {
      // Where an immediate correction is the CORRECT behavior. Latching here would defer
      // corrections that should be written, which is the bug in the other direction.
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      for (const [t, pos] of [[100, 10], [200, 20], [300, 30]] as const) {
        clock = t
        v.sample(pos) // 0.1 px/ms throughout
      }
      expect(v.isCoasting()).toBe(false)
    })

    it('is false before a fling establishes it, even on the unknown cold seed', () => {
      const clock = 0
      const v = make(() => clock)
      expect(v.isCoasting()).toBe(false) // unseeded
      v.sample(0)
      // One sample cannot establish a speed (velocity is the Infinity seed), and isFling's
      // defer-bias reports true here -- isCoasting must not inherit that bias.
      expect(v.isFling()).toBe(true)
      expect(v.isCoasting()).toBe(false)
    })

    it('drops the latch when the user supplies a fresh wheel notch', () => {
      // A brisk deliberate wheel scroll can cross the threshold for one sample. Without
      // noteUserInput that sample would latch a coast that never happens, and every slower
      // sample after it would hold the latch for the rest of the gesture.
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      clock = 10
      v.sample(100) // one brisk notch measures as a fling
      expect(v.isCoasting()).toBe(true)
      v.noteUserInput() // ...but the user drove it, so it is not inertia
      expect(v.isCoasting()).toBe(false)
      clock = 20
      v.sample(105) // the gesture continues slowly and must not re-latch
      expect(v.isCoasting()).toBe(false)
    })

    it('re-latches from a real coast\'s own samples after the last wheel notch', () => {
      // The counterpart: a trackpad flick ends with a wheel event, then the OS drives the
      // coast with bare scroll events. Those fast samples must latch it again.
      let clock = 0
      const v = make(() => clock)
      v.sample(0)
      v.noteUserInput() // finger-on wheel event
      clock = 16
      v.sample(120) // 7.5 px/ms, driven by the OS -- no further wheel input
      expect(v.isCoasting()).toBe(true)
      clock = 32
      v.sample(150) // 1.875 px/ms, decaying
      expect(v.isCoasting()).toBe(true)
    })
  })
})
