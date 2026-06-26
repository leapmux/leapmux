import { describe, expect, it } from 'vitest'
import { createScrollVelocity } from './chatScrollVelocity'

describe('chatscrollvelocity', () => {
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
})
