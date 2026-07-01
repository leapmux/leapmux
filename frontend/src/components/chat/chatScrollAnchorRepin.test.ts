import type { ScrollAnchor } from '~/stores/chatTypes'
import { describe, expect, it, vi } from 'vitest'
import { createAnchorRepin } from './chatScrollAnchorRepin'

/** A fake scroll element exposing only the geometry the engine reads. */
function fakeEl(over: { scrollTop?: number, clientHeight?: number, scrollHeight?: number } = {}) {
  return {
    scrollTop: over.scrollTop ?? 0,
    clientHeight: over.clientHeight ?? 500,
    scrollHeight: over.scrollHeight ?? 5000, // maxScrollTop = 4500 > 0 by default
  } as unknown as HTMLDivElement
}

const anchorAt = (id: string): ScrollAnchor => ({ id, offsetWithinRow: 0 })

function setup(geometry: { scrollTop?: number, clientHeight?: number, scrollHeight?: number } = {}) {
  const el = fakeEl(geometry)
  const writes: number[] = []
  const flingSettle = { accumulate: vi.fn(), reset: vi.fn(), rebase: vi.fn() }
  const velocity = {
    isFling: vi.fn(() => false),
    isActivelyFlinging: vi.fn(() => false),
    hasRecentMomentumInput: vi.fn(() => false),
  }
  const virt = {
    anchorAt: vi.fn((top: number): ScrollAnchor | null => anchorAt(`top@${top}`)),
    scrollTopForAnchor: vi.fn((): number | null => 100),
  }
  let animating = false
  let userScrolling = false
  const onRepinClamp = vi.fn()
  const onAnchorDrift = vi.fn()
  const repin = createAnchorRepin({
    getEl: () => el,
    virt,
    isAnimating: () => animating,
    writeScrollTop: (t) => {
      writes.push(t)
      el.scrollTop = t
    },
    velocity,
    flingSettle: () => flingSettle,
    isUserScrolling: () => userScrolling,
    onRepinClamp,
    onAnchorDrift,
  })
  return {
    repin,
    el,
    writes,
    flingSettle,
    velocity,
    virt,
    onRepinClamp,
    onAnchorDrift,
    setAnimating: (v: boolean) => { animating = v },
    setUserScrolling: (v: boolean) => { userScrolling = v },
  }
}

describe('createanchorrepin state machine', () => {
  it('starts following the tail with no anchor', () => {
    const { repin } = setup()
    expect(repin.isFollowing()).toBe(true)
    expect(repin.currentAnchor()).toBe(null)
  })

  it('setAnchor pins a row; followTail and setAnchor(null) return to following', () => {
    const { repin } = setup()
    repin.setAnchor(anchorAt('m1'))
    expect(repin.isFollowing()).toBe(false)
    expect(repin.currentAnchor()).toEqual(anchorAt('m1'))

    repin.followTail()
    expect(repin.isFollowing()).toBe(true)
    expect(repin.currentAnchor()).toBe(null)

    repin.setAnchor(anchorAt('m2'))
    repin.setAnchor(null) // empty list -> following
    expect(repin.isFollowing()).toBe(true)
  })

  it('captureAnchor pins the viewport-midpoint row and rebases fling-settle', () => {
    const { repin, el, virt, flingSettle } = setup()
    el.scrollTop = 200
    repin.captureAnchor()
    expect(virt.anchorAt).toHaveBeenCalledWith(450)
    expect(repin.currentAnchor()).toEqual(anchorAt('top@450'))
    expect(flingSettle.rebase).toHaveBeenCalledTimes(1)
  })

  it('captureTopAnchor pins the viewport-top row and rebases fling-settle', () => {
    const { repin, el, virt, flingSettle } = setup()
    el.scrollTop = 200
    repin.captureTopAnchor()
    expect(virt.anchorAt).toHaveBeenCalledWith(200)
    expect(repin.currentAnchor()).toEqual(anchorAt('top@200'))
    expect(flingSettle.rebase).toHaveBeenCalledTimes(1)
  })

  it('captureRowTopAnchor pins the given row TOP (offsetWithinRow 0) and rebases fling-settle', () => {
    const { repin, el, virt, flingSettle } = setup({ clientHeight: 500 })
    el.scrollTop = 100
    virt.scrollTopForAnchor.mockReturnValue(300) // the toggled row's top sits at content-Y 300
    repin.captureRowTopAnchor('m5')
    // Resolved by ID at the row's own top, NOT the viewport midpoint (which would be 350).
    expect(virt.scrollTopForAnchor).toHaveBeenCalledWith({ id: 'm5', offsetWithinRow: 0 })
    expect(repin.currentAnchor()).toEqual({ id: 'm5', offsetWithinRow: 0 })
    expect(flingSettle.rebase).toHaveBeenCalledTimes(1)
  })

  it('keeps the toggled row stationary (no scroll) when its own height changes, even above the midpoint', () => {
    // The bug: a row whose top sits ABOVE the viewport midpoint. A viewport-midpoint anchor
    // (captureAnchor) lets the row scroll away as the re-pin compensates the growth that
    // happened above the midpoint -- the sibling midpoint test above writes 200 for exactly
    // this shape. Pinning the row's OWN top instead resolves to the same scrollTop -> no
    // write, so the toggled row stays where the user clicked.
    const { repin, el, writes, virt } = setup({ clientHeight: 500 })
    el.scrollTop = 1000
    virt.scrollTopForAnchor.mockReturnValue(1100) // the toggled row's top is 100px below the viewport top
    repin.captureRowTopAnchor('tool-result')
    // Expanding changes only THIS row's height; rows above don't move, so its top offset is
    // unchanged -- scrollTopForAnchor still resolves to 1100. targetTop = 1100 - 500*0.2 = 1000.
    repin.repinToAnchor()
    expect(writes).toEqual([])
  })

  it('captureRowTopAnchor clamps the pin ratio to 0 when the row top sits above the viewport top', () => {
    // Defensive clamp (the header holding the toggle button is non-sticky, so at click time
    // the row top is normally on-screen). A row top ABOVE the viewport top -> ratio 0 pins
    // the row top to the viewport top rather than storing a negative offset.
    const { repin, el, writes, virt } = setup({ clientHeight: 500 })
    el.scrollTop = 400
    virt.scrollTopForAnchor.mockReturnValue(300) // 100px above the viewport top -> ratio clamps to 0
    repin.captureRowTopAnchor('m1')
    repin.repinToAnchor()
    expect(writes).toEqual([300]) // targetTop = rowTop - clientHeight*0
  })

  it('captureRowTopAnchor is a no-op when the row is not in the offset map (trimmed away)', () => {
    const { repin, flingSettle, virt } = setup()
    virt.scrollTopForAnchor.mockReturnValue(null)
    repin.captureRowTopAnchor('gone')
    expect(repin.isFollowing()).toBe(true) // scroll mode untouched
    expect(flingSettle.rebase).not.toHaveBeenCalled()
  })

  it('captureRowTopAnchor is a no-op when the viewport has no height', () => {
    const { repin, virt, flingSettle } = setup({ clientHeight: 0 })
    repin.captureRowTopAnchor('m1')
    expect(repin.isFollowing()).toBe(true)
    expect(virt.scrollTopForAnchor).not.toHaveBeenCalled() // bailed before reading the offset
    expect(flingSettle.rebase).not.toHaveBeenCalled()
  })

  it('captureAnchor pins the TOP row (ratio 0) at the very top edge, not the midpoint', () => {
    const { repin, el, writes, virt } = setup()
    el.scrollTop = 0 // at the very top edge (<= EDGE_INTENT_TOLERANCE_PX)
    repin.captureAnchor()
    // The top ROW (offset 0), not the viewport-midpoint row (which would be 250).
    expect(virt.anchorAt).toHaveBeenCalledWith(0)
    expect(repin.currentAnchor()).toEqual(anchorAt('top@0'))
    // Held at ratio 0: resolving the row writes scrollTop to its exact offset with no
    // midpoint subtraction (a midpoint anchor would write 300 - 250 = 50), so a taller
    // top-row measurement can't drift the view down off the top.
    virt.scrollTopForAnchor.mockReturnValue(300)
    repin.repinToAnchor()
    expect(writes).toEqual([300])
  })

  it('keeps a captured viewport-midpoint row stationary when it re-pins', () => {
    const { repin, el, writes, virt } = setup()
    el.scrollTop = 100
    repin.captureAnchor() // captures 350: scrollTop 100 + clientHeight/2 250
    virt.scrollTopForAnchor.mockReturnValue(450) // content above the midpoint grew by 100

    repin.repinToAnchor()

    expect(writes).toEqual([200]) // 450 - midpoint offset 250
  })

  it('keeps a captured viewport-top row pinned to the top when it re-pins', () => {
    const { repin, el, writes, virt } = setup()
    el.scrollTop = 0
    repin.captureTopAnchor()
    virt.scrollTopForAnchor.mockReturnValue(120)

    repin.repinToAnchor()

    expect(writes).toEqual([120])
  })
})

describe('createanchorrepin repinToAnchor decision', () => {
  it('does nothing while following the tail (no anchor to pin)', () => {
    const { repin, writes, virt } = setup()
    repin.repinToAnchor()
    expect(virt.scrollTopForAnchor).not.toHaveBeenCalled()
    expect(writes).toEqual([])
  })

  it('writes scrollTop to keep an anchored row stationary (the common keep-position case)', () => {
    const { repin, el, writes, virt } = setup()
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1')) // captured at scrollTop 0
    virt.scrollTopForAnchor.mockReturnValue(100) // a prepend above shifted it to 100
    repin.repinToAnchor()
    expect(writes).toEqual([100])
  })

  it('skips a sub-threshold correction (a measurement below the anchor)', () => {
    const { repin, el, writes, virt } = setup()
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(0.5) // delta 0.5 < REPIN_MIN_DELTA_PX (1)
    repin.repinToAnchor()
    expect(writes).toEqual([])
  })

  it('re-anchors (no write) when the viewport was flung more than a screen from the capture', () => {
    const { repin, el, writes, virt, flingSettle } = setup()
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1')) // captured at 0
    el.scrollTop = 600 // flung 600 > clientHeight 500 since capture
    virt.scrollTopForAnchor.mockReturnValue(50) // a stale resolve the re-pin would yank back to
    repin.repinToAnchor()
    expect(writes).toEqual([]) // left the fling intact
    expect(repin.currentAnchor()).toEqual(anchorAt('top@850')) // re-anchored to the live viewport row
    expect(flingSettle.reset).toHaveBeenCalled()
  })

  it('writes a large keep-position correction over a stationary viewport (a page prepend), even mid-fling', () => {
    // The regression case: a page-sized prepend/trim shifts the anchor by 300px while the
    // viewport itself has NOT moved since capture (movedSinceCapture 0, not flungAway). This
    // is a real keep-position shift and MUST be written -- dropping it (the old
    // `activeFling && delta >= flingSuppressPx` branch) leaked the whole shift as scroll
    // drift. A live fling does not change that: compensating the prepend keeps content put.
    const { repin, el, writes, virt, velocity, flingSettle } = setup()
    velocity.isFling.mockReturnValue(true) // an active fling is in progress
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1')) // captured at scrollTop 0 -> viewport stationary
    virt.scrollTopForAnchor.mockReturnValue(300) // delta 300 >= flingSuppressPx (250)
    repin.repinToAnchor()
    expect(writes).toEqual([300]) // compensated, NOT dropped
    expect(repin.currentAnchor()).toEqual(anchorAt('m1')) // still pinned to the same row
    expect(flingSettle.reset).toHaveBeenCalled() // the write supersedes any deferred drift
  })

  it('accumulates a small correction during a user fling instead of writing', () => {
    const { repin, el, writes, virt, velocity, flingSettle, setUserScrolling } = setup()
    setUserScrolling(true)
    velocity.isFling.mockReturnValue(true)
    velocity.isActivelyFlinging.mockReturnValue(true)
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(50) // delta 50 < flingSuppressPx, >= REPIN_MIN
    repin.repinToAnchor()
    expect(writes).toEqual([]) // deferred, not written mid-fling
    expect(flingSettle.accumulate).toHaveBeenCalledWith(50) // signed shift accumulated
  })

  it('accumulates a small correction during an active fling even after the scroll handler returns', () => {
    const { repin, el, writes, virt, velocity, flingSettle } = setup()
    velocity.isActivelyFlinging.mockReturnValue(true)
    el.scrollTop = 3000
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(2918) // measurement shrink above the anchor

    repin.repinToAnchor()

    expect(writes).toEqual([])
    expect(flingSettle.accumulate).toHaveBeenCalledWith(-82)
  })

  it('writes a small correction once momentum has stopped, even within the input-grace window', () => {
    // The post-fling SETTLE: momentum is no longer moving the viewport (isActivelyFlinging
    // false), but the 750ms momentum-input grace still holds and the velocity sample has
    // gone stale (isFling's idle default -> true). A look-ahead premeasure lands here and
    // shrinks content above the anchor. Because the viewport is STATIONARY, a scrollTop
    // write cancels no momentum -- so correct it immediately (an off-screen, invisible
    // shift) rather than deferring it into drift that accumulates across the settle burst
    // (the observed run of deferred-fling WARNs climbing to ~176px at a fixed scrollTop).
    const { repin, el, writes, virt, velocity, flingSettle } = setup({ clientHeight: 733, scrollHeight: 6000 })
    velocity.isActivelyFlinging.mockReturnValue(false)
    velocity.hasRecentMomentumInput.mockReturnValue(true)
    velocity.isFling.mockReturnValue(true)
    el.scrollTop = 4606
    repin.setAnchor(anchorAt('m1')) // captured at the live position -> viewport stationary
    virt.scrollTopForAnchor.mockReturnValue(4592) // a premeasure shrank content above by 14

    repin.repinToAnchor()

    expect(writes).toEqual([4592])
    expect(flingSettle.accumulate).not.toHaveBeenCalled()
  })

  it('does not write a momentum-tail correction after the scroll handler returns', () => {
    const { repin, el, writes, virt, velocity, flingSettle } = setup({ clientHeight: 733 })
    velocity.hasRecentMomentumInput.mockReturnValue(true)
    el.scrollTop = 2677
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(2651) // low-velocity momentum tail measurement

    repin.repinToAnchor()

    expect(writes).toEqual([])
    expect(repin.currentAnchor()).toEqual(anchorAt('top@3043.5'))
    expect(flingSettle.reset).toHaveBeenCalled()
  })

  it('re-anchors instead of writing a small correction during a slow user scroll', () => {
    const { repin, el, writes, virt, flingSettle, setUserScrolling } = setup()
    setUserScrolling(true)
    el.scrollTop = 100
    repin.setAnchor(anchorAt('m1')) // captured at scrollTop 100
    virt.scrollTopForAnchor.mockReturnValue(116) // a small measurement correction above the anchor

    repin.repinToAnchor()

    expect(writes).toEqual([])
    expect(repin.currentAnchor()).toEqual(anchorAt('top@350'))
    expect(flingSettle.reset).toHaveBeenCalled()
  })

  it('re-anchors instead of writing a medium estimate correction during a slow native scroll', () => {
    const { repin, el, writes, virt, flingSettle, setUserScrolling } = setup({ clientHeight: 733, scrollHeight: 6000 })
    setUserScrolling(true)
    el.scrollTop = 4397
    repin.setAnchor(anchorAt('m1')) // captured at the live viewport position
    virt.scrollTopForAnchor.mockReturnValue(4319) // 78px, like a tool row measuring shorter

    repin.repinToAnchor()

    expect(writes).toEqual([])
    expect(repin.currentAnchor()).toEqual(anchorAt('top@4763.5'))
    expect(flingSettle.reset).toHaveBeenCalled()
  })

  it('still writes a large correction during a slow user scroll', () => {
    const { repin, el, writes, virt, setUserScrolling } = setup()
    setUserScrolling(true)
    el.scrollTop = 100
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(260)

    repin.repinToAnchor()

    expect(writes).toEqual([260])
  })

  it('skips the write when the content fits the viewport (nothing to scroll)', () => {
    // scrollHeight 400 < clientHeight 500 -> maxScrollTop 0 (the content fits).
    const { repin, el, writes, virt } = setup({ scrollHeight: 400 })
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(100)
    repin.repinToAnchor()
    expect(writes).toEqual([])
  })

  it('re-anchors when the pinned row no longer resolves (trimmed / reseq under a new id)', () => {
    const { repin, el, writes, virt, flingSettle } = setup()
    el.scrollTop = 50
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(null) // anchor gone
    repin.repinToAnchor()
    expect(writes).toEqual([])
    expect(repin.currentAnchor()).toEqual(anchorAt('top@300')) // re-anchored to the surviving viewport-midpoint row
    expect(flingSettle.reset).toHaveBeenCalled()
  })

  it('re-anchors to the TOP row (ratio 0) at the top edge when the pinned row no longer resolves', () => {
    // A Home jump replaces the loaded window; the anchored top row no longer resolves.
    // Recovery at the top edge must land on the TOP row, not the viewport midpoint --
    // otherwise a taller top-row measurement re-centers the midpoint and drifts the view
    // a viewport-fraction below the top (the long-transcript Home symptom).
    const { repin, el, writes, virt, flingSettle } = setup()
    el.scrollTop = 0 // parked at the very top edge
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(null) // the pinned row was trimmed / re-fetched away
    repin.repinToAnchor()
    expect(writes).toEqual([])
    expect(virt.anchorAt).toHaveBeenLastCalledWith(0) // top row, not the midpoint (250)
    expect(repin.currentAnchor()).toEqual(anchorAt('top@0'))
    expect(flingSettle.reset).toHaveBeenCalled()

    // Held at ratio 0: the recovered row resolves straight to its offset (no midpoint
    // subtraction, which would have written 50).
    virt.scrollTopForAnchor.mockReturnValue(300)
    repin.repinToAnchor()
    expect(writes).toEqual([300])
  })
})

describe('createanchorrepin deferred-during-animation', () => {
  it('defers a re-pin while an animation is running and applies it on a mid-flight cancel', () => {
    const { repin, el, writes, virt, setAnimating } = setup()
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(100)

    setAnimating(true)
    repin.repinToAnchor()
    expect(writes).toEqual([]) // deferred, not written against the animation

    setAnimating(false)
    repin.applyDeferredRepinOnCancel()
    expect(writes).toEqual([100]) // the deferred shift is absorbed now
  })

  it('resetDeferredRepin drops a deferred re-pin without applying it (natural animation end)', () => {
    const { repin, el, writes, virt, setAnimating } = setup()
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(100)

    setAnimating(true)
    repin.repinToAnchor() // defers

    setAnimating(false)
    repin.resetDeferredRepin() // natural end (stuck to bottom) absorbed it
    repin.applyDeferredRepinOnCancel() // nothing left to apply
    expect(writes).toEqual([])
  })
})

describe('createanchorrepin clamp reporting (onRepinClamp)', () => {
  it('reports a top-edge clamp: the keep-position target went negative and the row jumped up', () => {
    const { repin, el, writes, virt, onRepinClamp } = setup({ clientHeight: 500, scrollHeight: 5000 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m1'), 200, 0.5) // pinned at the viewport midpoint, captured at scrollTop 200
    virt.scrollTopForAnchor.mockReturnValue(100) // row top at content-Y 100 -> idealTop = 100 - 250 = -150
    repin.repinToAnchor()
    expect(writes).toEqual([0]) // clamped to the top edge
    expect(onRepinClamp).toHaveBeenCalledTimes(1)
    expect(onRepinClamp).toHaveBeenCalledWith(expect.objectContaining({
      anchorId: 'm1',
      clampPx: 150, // 0 - (-150): row pushed 150px up from its captured line
      idealTop: -150,
      targetTop: 0,
      fromTop: 200,
      clientHeight: 500,
      maxScrollTop: 4500,
    }))
  })

  it('reports a bottom-edge clamp: the target exceeded maxScrollTop and the row jumped down', () => {
    const { repin, el, writes, virt, onRepinClamp } = setup({ clientHeight: 500, scrollHeight: 5000 })
    el.scrollTop = 4300
    repin.setAnchor(anchorAt('m9'), 4300, 0.5)
    virt.scrollTopForAnchor.mockReturnValue(5000) // idealTop = 5000 - 250 = 4750 > maxScrollTop 4500
    repin.repinToAnchor()
    expect(writes).toEqual([4500]) // clamped to the bottom edge
    expect(onRepinClamp).toHaveBeenCalledTimes(1)
    expect(onRepinClamp).toHaveBeenCalledWith(expect.objectContaining({
      anchorId: 'm9',
      clampPx: -250, // 4500 - 4750: row pushed 250px down from its captured line
      idealTop: 4750,
      targetTop: 4500,
      maxScrollTop: 4500,
    }))
  })

  it('does not report when the keep-position write lands unclamped', () => {
    const { repin, el, writes, virt, onRepinClamp } = setup({ clientHeight: 500, scrollHeight: 5000 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m1'), 200, 0.5)
    virt.scrollTopForAnchor.mockReturnValue(1100) // idealTop = 1100 - 250 = 850, well within [0, 4500]
    repin.repinToAnchor()
    expect(writes).toEqual([850])
    expect(onRepinClamp).not.toHaveBeenCalled()
  })

  it('does not report a sub-REPIN_MIN_DELTA_PX clamp (imperceptible boundary rounding)', () => {
    const { repin, el, writes, virt, onRepinClamp } = setup({ clientHeight: 500, scrollHeight: 5000 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m1'), 200, 0.5)
    virt.scrollTopForAnchor.mockReturnValue(249.5) // idealTop = -0.5 -> clamps to 0, clampPx 0.5 < 1px
    repin.repinToAnchor()
    expect(writes).toEqual([0])
    expect(onRepinClamp).not.toHaveBeenCalled()
  })
})

describe('createanchorrepin drift reporting (onAnchorDrift)', () => {
  it('reports an ABSORBED slow-scroll correction (uncorrected on-screen shift)', () => {
    const { repin, el, writes, virt, onAnchorDrift, setUserScrolling } = setup({ clientHeight: 500 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m'), 200, 0) // captured at the viewport top (ratio 0)
    virt.scrollTopForAnchor.mockReturnValue(250) // row top now 250 -> ideal 250, a +50px correction
    setUserScrolling(true) // a slow user scroll is in progress
    repin.repinToAnchor()
    // The 50px correction (<= the small-absorb cap) is ABSORBED, not written: the anchored
    // row is left displaced 50px on-screen, reported as drift.
    expect(writes).toEqual([])
    expect(onAnchorDrift).toHaveBeenCalledTimes(1)
    expect(onAnchorDrift).toHaveBeenCalledWith(expect.objectContaining({
      anchorId: 'm',
      residualPx: 50,
      reason: 'absorbed',
      fromTop: 200,
      clientHeight: 500,
    }))
  })

  it('reports a DEFERRED fling correction as drift', () => {
    const { repin, el, writes, virt, onAnchorDrift, flingSettle, velocity } = setup({ clientHeight: 500 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m'), 200, 0)
    virt.scrollTopForAnchor.mockReturnValue(250) // +50px correction (< flingSuppressPx 250)
    velocity.isActivelyFlinging.mockReturnValue(true) // a fast fling is running
    velocity.isFling.mockReturnValue(true)
    repin.repinToAnchor()
    expect(writes).toEqual([]) // deferred, not written
    expect(flingSettle.accumulate).toHaveBeenCalledWith(50)
    expect(onAnchorDrift).toHaveBeenCalledWith(expect.objectContaining({
      residualPx: 50,
      reason: 'deferred-fling',
    }))
  })

  it('does NOT report drift on an immediate keep-position write (idle: the correction is applied)', () => {
    const { repin, el, writes, virt, onAnchorDrift } = setup({ clientHeight: 500 })
    el.scrollTop = 200
    repin.setAnchor(anchorAt('m'), 200, 0)
    virt.scrollTopForAnchor.mockReturnValue(250) // +50px correction, idle -> written immediately
    repin.repinToAnchor()
    expect(writes).toEqual([250]) // corrected, so the row stays on its line -- no drift
    expect(onAnchorDrift).not.toHaveBeenCalled()
  })
})
