import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { describe, expect, it, vi } from 'vitest'

import { inferScrollDirection } from './chatScrollGeometry'
import { createScrollInput } from './chatScrollInput'
import { createScrollVelocity } from './chatScrollVelocity'
import { computeBufferAwareKeepNewest, computeKeepNewest } from './useChatScroll'
import { installScrollTestEnv, makeScrollContext } from './useChatScroll.testkit'

installScrollTestEnv()

describe('createscrollvelocity', () => {
  // A controllable clock so velocity is deterministic (no real timing).
  function fakeClock(start = 0) {
    let t = start
    return {
      now: () => t,
      advance: (ms: number) => {
        t += ms
      },
    }
  }
  const make = (clock: { now: () => number }) =>
    createScrollVelocity({ now: clock.now, thresholdPxPerMs: 1, idleMs: 150 })

  it('reports a fling when velocity is unknown (no sample / one sample yet)', () => {
    const clock = fakeClock()
    const v = make(clock)
    // No sample: the first event of a gesture has no prior to measure against, so
    // it defers (preserves the prior always-defer behavior).
    expect(v.isFling()).toBe(true)
    v.sample(0)
    // One sample still can't bound a speed.
    expect(v.isFling()).toBe(true)
  })

  it('reports a fling for a fast cadence and not for a slow one', () => {
    const fast = fakeClock()
    const vf = make(fast)
    vf.sample(0)
    fast.advance(16)
    vf.sample(80) // 80px / 16ms = 5 px/ms >= 1 -> fling
    expect(vf.isFling()).toBe(true)

    const slow = fakeClock()
    const vs = make(slow)
    vs.sample(0)
    slow.advance(100)
    vs.sample(8) // 8px / 100ms = 0.08 px/ms < 1 -> deliberate scroll
    expect(vs.isFling()).toBe(false)
  })

  it('treats the threshold as inclusive (>= is a fling)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(10)
    v.sample(10) // exactly 1 px/ms
    expect(v.isFling()).toBe(true)
  })

  it('reports a fling again once the last sample goes stale (momentum has stopped)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(50)
    v.sample(4) // 0.08 px/ms -> slow
    expect(v.isFling()).toBe(false)
    // No new events for longer than idleMs: the slow reading is stale, so a fresh
    // gesture must not inherit it -- default back to deferring.
    clock.advance(200)
    expect(v.isFling()).toBe(true)
  })

  it('drops a same-tick (dt <= 0) sample without dividing by zero or absorbing its jump', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    v.sample(500) // same tick: dropped -> velocity stays unknown (no spurious Infinity flip)
    expect(v.isFling()).toBe(true)
  })

  it('measures the next interval from the last TIMED position across a coalesced sample', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(100) // t=16: 100/16 = 6.25 px/ms, lastTime=16, lastPos=100
    // A coalesced re-emit at the SAME tick must NOT become the baseline -- otherwise
    // the next interval would measure 220-160 (absorbed jump) and under-report speed.
    v.sample(160) // same tick t=16: dropped
    clock.advance(16)
    v.sample(220) // t=32: |220 - 100| / 16 = 7.5 px/ms (from the last timed pos, span included)
    expect(v.speed()).toBe(7.5)
  })

  it('excludes a programmatic displacement from the measured gesture (no false fling)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0) // seed
    clock.advance(50)
    v.sample(4) // a slow deliberate scroll -> not a fling
    expect(v.isFling()).toBe(false)
    // A re-pin writes scrollTop +500 (a big prepend displacement in a hidden-heavy
    // window) -- NOT user gesture. The baseline moves with it so it doesn't count.
    v.syncToProgrammatic(504)
    clock.advance(100)
    // The user creeps another 50px: gesture = 50px/100ms = 0.5 px/ms, under the 1
    // px/ms threshold. WITHOUT the sync the delta would be (504+50)-4 = 550px over
    // 100ms = 5.5 px/ms and misclassify as a fling, deferring corrections that then
    // land as one overshoot.
    v.sample(554)
    expect(v.isFling()).toBe(false)
  })

  it('syncToProgrammatic is a no-op before the first sample (no baseline to move)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.syncToProgrammatic(1000) // nothing seeded yet
    expect(v.isFling()).toBe(true) // unknown velocity still defers
    clock.advance(16)
    v.sample(0)
    clock.advance(16)
    v.sample(200) // 200px / 16ms -> a genuine fast gesture is still a fling
    expect(v.isFling()).toBe(true)
  })

  // isActivelyFlinging differs from isFling: it is FALSE for the unknown seed and
  // while idle, so an async re-pin only abandons keep-position for a genuine live
  // fling whose momentum a write would cancel.
  it('isActivelyFlinging is false for the unknown (Infinity) seed, unlike isFling', () => {
    const clock = fakeClock()
    const v = make(clock)
    expect(v.isFling()).toBe(true)
    expect(v.isActivelyFlinging()).toBe(false) // nothing measured yet -> not a live fling
    v.sample(0)
    expect(v.isFling()).toBe(true)
    expect(v.isActivelyFlinging()).toBe(false) // one sample still can't bound a speed
  })

  it('isActivelyFlinging is true only for a measured, in-flight fast fling', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(80) // 5 px/ms -> a measured fast fling
    expect(v.isActivelyFlinging()).toBe(true)
    // Momentum stops: no events for longer than idleMs -> no longer active.
    clock.advance(200)
    expect(v.isActivelyFlinging()).toBe(false)
    expect(v.isFling()).toBe(true) // isFling defaults back to defer; isActivelyFlinging does not
  })

  it('isActivelyFlinging is false for a slow deliberate scroll', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(100)
    v.sample(8) // 0.08 px/ms < 1 -> not a fling
    expect(v.isActivelyFlinging()).toBe(false)
  })

  // speed() drives the render-ahead overscan, so it must mirror isActivelyFlinging's
  // gating: 0 for the unknown seed and once idle, the measured px/ms otherwise.
  it('speed reports 0 until a velocity is measured, then the measured px/ms', () => {
    const clock = fakeClock()
    const v = make(clock)
    expect(v.speed()).toBe(0) // no sample -> no look-ahead
    v.sample(0)
    expect(v.speed()).toBe(0) // one sample: velocity is still the unknown Infinity seed
    clock.advance(16)
    v.sample(80) // 80px / 16ms = 5 px/ms
    expect(v.speed()).toBe(5)
  })

  it('speed decays to 0 once momentum goes stale (no render-ahead when idle)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(80) // 5 px/ms
    expect(v.speed()).toBe(5)
    clock.advance(200) // longer than idleMs (150): the reading is stale
    expect(v.speed()).toBe(0)
  })
})

describe('inferScrollDirection', () => {
  it('returns older when scrollTop moved up (toward older history)', () => {
    expect(inferScrollDirection(500, 300)).toBe('older')
  })

  it('returns newer when scrollTop moved down', () => {
    expect(inferScrollDirection(300, 500)).toBe('newer')
  })

  it('returns null when the position did not change (no direction to infer)', () => {
    expect(inferScrollDirection(420, 420)).toBeNull()
  })

  it('treats a one-pixel delta as a direction (scrollbar nudge / momentum tail)', () => {
    expect(inferScrollDirection(100, 101)).toBe('newer')
    expect(inferScrollDirection(100, 99)).toBe('older')
  })
})

describe('computekeepnewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [msg('m1', 1n), msg('m2', 2n), msg('m3', 3n), msg('m4', 4n)]

  it('keeps 0 when following the tail (no anchor), so the store applies the normal cap', () => {
    expect(computeKeepNewest(msgs, null, -1)).toBe(0)
  })

  it('keeps the whole window when the anchor is set but unresolvable (displaced / empty)', () => {
    expect(computeKeepNewest(msgs, anchor('gone'), -1)).toBe(msgs.length)
  })

  it('keeps the server rows from the anchor down to the tail', () => {
    // Anchor at index 1 (m2): keep m2, m3, m4 -> 3 rows.
    expect(computeKeepNewest(msgs, anchor('m2'), 1)).toBe(3)
  })

  it('excludes trailing optimistic locals (seq 0n) from the kept-server count', () => {
    // m1, m2 server + two trailing locals; anchor at m1 (index 0). The store caps
    // server messages only, so the locals must NOT inflate the kept count.
    const withLocals = [msg('m1', 1n), msg('m2', 2n), msg('local-a', 0n), msg('local-b', 0n)]
    expect(computeKeepNewest(withLocals, anchor('m1'), 0)).toBe(2)
  })
})

describe('computebufferawarekeepnewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [msg('m1', 1n), msg('m2', 2n), msg('m3', 3n), msg('m4', 4n)]
  const mustNotResolve = (): null => {
    throw new Error('anchorAt should not be called')
  }

  it('keeps the lean base cap (0) for a hidden tab (clientHeight 0), without resolving an anchor', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 0, 200, mustNotResolve)).toBe(0)
  })

  it('keeps the whole window when less than a buffer of visible content sits above the top', () => {
    // scrollTop 150 - bufferTargetPx 200 = -50 <= 0 -> keep all (never reap surfaced content).
    expect(computeBufferAwareKeepNewest(msgs, 150, 500, 200, mustNotResolve)).toBe(msgs.length)
  })

  it('delegates to computeKeepNewest from the resolved buffer-top anchor', () => {
    // bufTop = 1000 - 200 = 800; anchorAt resolves to m2 (index 1) -> keep m2..m4 = 3.
    let askedBufTop = -1
    const got = computeBufferAwareKeepNewest(msgs, 1000, 500, 200, (bufTop) => {
      askedBufTop = bufTop
      return anchor('m2')
    })
    expect(askedBufTop).toBe(800)
    expect(got).toBe(3)
  })

  it('keeps the whole window when the buffer top is unresolvable (anchorAt null)', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 500, 200, () => null)).toBe(msgs.length)
  })
})

describe('createscrollinput', () => {
  function fakeEl(opts: { clientHeight?: number, scrollTop?: number, clamp?: boolean } = {}) {
    const el = {
      clientHeight: opts.clientHeight ?? 500,
      scrollTop: opts.scrollTop ?? 1000,
      // clamp:true models an at-edge container where scrollBy can't move.
      scrollBy: ({ top }: { top: number }) => {
        if (!opts.clamp)
          el.scrollTop += top
      },
    }
    return el as unknown as HTMLDivElement
  }

  function setup(el: HTMLDivElement | undefined) {
    const calls = {
      lastScrollDir: [] as Array<'older' | 'newer'>,
      discretePageTarget: [] as Array<number | null>,
      tryOlder: 0,
      tryNewer: 0,
      forceBottom: 0,
      cancelPending: 0,
      captureAnchor: 0,
      captureTopAnchor: 0,
    }
    const input = createScrollInput(
      makeScrollContext({ getEl: () => el }),
      {
        captureAnchor: () => { calls.captureAnchor++ },
        captureTopAnchor: () => { calls.captureTopAnchor++ },
        checkAtBottom: () => {},
        forceScrollToBottom: () => { calls.forceBottom++ },
        cancelScrollAnimation: () => {},
        cancelPendingScroll: () => { calls.cancelPending++ },
        tryLoadOlderOnExplicitTopIntent: () => { calls.tryOlder++ },
        tryLoadNewerOnExplicitBottomIntent: () => { calls.tryNewer++ },
        setLastScrollDir: dir => calls.lastScrollDir.push(dir),
        setDiscretePageTarget: target => calls.discretePageTarget.push(target),
        hasOlderMessages: () => false,
        onJumpToOldest: undefined,
      },
    )
    return { input, calls }
  }

  it('pageScroll mid-list records the un-clamped target, scrolls, and does NOT directly load', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 1000 })
    const { input, calls } = setup(el)
    input.pageScroll(1)
    // delta = max(500-48, 250) = 452; target = 1000 + 452 = 1452.
    expect(calls.discretePageTarget).toEqual([1452])
    expect(el.scrollTop).toBe(1452) // moved -> the native scroll event owns the fill
    expect(calls.tryNewer).toBe(0)
    expect(calls.tryOlder).toBe(0)
  })

  it('pageScroll at an edge (no movement) clears the target and pages via the explicit-intent loader', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 0, clamp: true })
    const { input, calls } = setup(el)
    input.pageScroll(-1)
    // target -452 set, then cleared to null when scrollBy couldn't move. The direction
    // routes to the un-paused older-intent loader (NOT the pause-gated buffer filler).
    expect(calls.discretePageTarget).toEqual([-452, null])
    expect(calls.tryOlder).toBe(1)
    expect(calls.tryNewer).toBe(0)
  })

  it('pageScroll at the bottom edge pages newer via the explicit-intent loader', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 9999, clamp: true })
    const { input, calls } = setup(el)
    input.pageScroll(1)
    expect(calls.discretePageTarget).toEqual([10451, null])
    expect(calls.tryNewer).toBe(1)
    expect(calls.tryOlder).toBe(0)
  })

  it('pageScroll is a no-op on a 0-height (hidden) container', () => {
    const { input, calls } = setup(fakeEl({ clientHeight: 0 }))
    input.pageScroll(1)
    expect(calls.discretePageTarget).toEqual([])
    expect(calls.tryNewer).toBe(0)
    expect(calls.tryOlder).toBe(0)
  })

  it('handleKeyDown PageDown pages newer and prevents default', () => {
    const { input, calls } = setup(fakeEl())
    const preventDefault = vi.fn()
    input.handleKeyDown({ key: 'PageDown', preventDefault } as unknown as KeyboardEvent)
    expect(preventDefault).toHaveBeenCalled()
    expect(calls.lastScrollDir).toEqual(['newer'])
    expect(calls.discretePageTarget.length).toBeGreaterThan(0) // pageScroll ran
  })

  it('handleKeyDown End forces scroll to the live tail', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'End', preventDefault: () => {} } as KeyboardEvent)
    expect(calls.forceBottom).toBe(1)
    expect(calls.lastScrollDir).toEqual(['newer'])
  })

  it('handleKeyDown Home pins to the viewport-top anchor', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'Home', preventDefault: () => {} } as KeyboardEvent)
    expect(calls.captureTopAnchor).toBe(1)
    expect(calls.captureAnchor).toBe(0)
    expect(calls.lastScrollDir).toEqual(['older'])
  })

  it('handleKeyDown ignores modifier-chorded keys', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'PageDown', metaKey: true, preventDefault: () => {} } as KeyboardEvent)
    expect(calls.lastScrollDir).toEqual([])
    expect(calls.discretePageTarget).toEqual([])
  })

  it('handleWheel records the direction and triggers the edge-intent load by deltaY sign', () => {
    const { input, calls } = setup(fakeEl())
    input.handleWheel({ deltaY: -10, deltaX: 0 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older'])
    expect(calls.tryOlder).toBe(1)
    input.handleWheel({ deltaY: 10, deltaX: 0 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older', 'newer'])
    expect(calls.tryNewer).toBe(1)
  })

  it('handleWheel cancels the pending settle on a no-movement (0,0) event', () => {
    const { input, calls } = setup(fakeEl())
    input.handleWheel({ deltaY: 0, deltaX: 0 } as WheelEvent)
    expect(calls.cancelPending).toBe(1)
    expect(calls.lastScrollDir).toEqual([])
  })

  it('handleWheel ignores a horizontal-dominant swipe whose small deltaY leaks (no spurious intent)', () => {
    const { input, calls } = setup(fakeEl())
    // A sideways trackpad swipe: large horizontal delta, tiny vertical leak. It must
    // NOT fire an edge-intent load or set lastScrollDir (which would mis-steer the
    // buffer filler), and is not a (0,0) momentum-cancel either.
    input.handleWheel({ deltaY: -3, deltaX: 80 } as WheelEvent)
    input.handleWheel({ deltaY: 2, deltaX: -80 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual([])
    expect(calls.tryOlder).toBe(0)
    expect(calls.tryNewer).toBe(0)
    expect(calls.cancelPending).toBe(0)
    // A vertical-dominant diagonal still counts as intent.
    input.handleWheel({ deltaY: -80, deltaX: 3 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older'])
    expect(calls.tryOlder).toBe(1)
  })
})
