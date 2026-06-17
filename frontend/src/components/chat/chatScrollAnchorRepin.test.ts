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
  const velocity = { isFling: vi.fn(() => false) }
  const virt = {
    anchorAt: vi.fn((top: number): ScrollAnchor | null => anchorAt(`top@${top}`)),
    scrollTopForAnchor: vi.fn((): number | null => 100),
  }
  let animating = false
  let userScrolling = false
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
  })
  return {
    repin,
    el,
    writes,
    flingSettle,
    velocity,
    virt,
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

  it('captureAnchor pins the viewport-top row and rebases fling-settle', () => {
    const { repin, el, virt, flingSettle } = setup()
    el.scrollTop = 200
    repin.captureAnchor()
    expect(virt.anchorAt).toHaveBeenCalledWith(200)
    expect(repin.currentAnchor()).toEqual(anchorAt('top@200'))
    expect(flingSettle.rebase).toHaveBeenCalledTimes(1)
  })
})

describe('createanchorrepin repinToAnchor decision', () => {
  it('does nothing while following the tail (no anchor to pin)', () => {
    const { repin, writes } = setup()
    repin.repinToAnchor()
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
    expect(repin.currentAnchor()).toEqual(anchorAt('top@600')) // re-anchored to the live viewport row
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
    el.scrollTop = 0
    repin.setAnchor(anchorAt('m1'))
    virt.scrollTopForAnchor.mockReturnValue(50) // delta 50 < flingSuppressPx, >= REPIN_MIN
    repin.repinToAnchor()
    expect(writes).toEqual([]) // deferred, not written mid-fling
    expect(flingSettle.accumulate).toHaveBeenCalledWith(50) // signed shift accumulated
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
    expect(repin.currentAnchor()).toEqual(anchorAt('top@50')) // re-anchored to the surviving viewport-top row
    expect(flingSettle.reset).toHaveBeenCalled()
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
