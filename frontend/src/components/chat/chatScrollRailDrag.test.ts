import type { ThumbDragDeps } from './chatScrollRailDrag'
import type { PreparedGeometry } from './chatScrollRailGeometry'
import type { VirtualItem } from './useChatVirtualizer'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createThumbDrag } from './chatScrollRailDrag'
import { prepareGeometry } from './chatScrollRailGeometry'

// A hand-driven rAF so moves flush deterministically: requestAnimationFrame queues the
// callback (returning an id), cancelAnimationFrame drops it by id, flushRaf() runs the rest.
let rafQueue: { id: number, cb: FrameRequestCallback }[] = []
let nextRafId = 0
let rafSpy: ReturnType<typeof vi.fn>
let cancelSpy: ReturnType<typeof vi.fn>

beforeEach(() => {
  rafQueue = []
  nextRafId = 0
  rafSpy = vi.fn((cb: FrameRequestCallback) => {
    const id = ++nextRafId
    rafQueue.push({ id, cb })
    return id
  })
  cancelSpy = vi.fn((id: number) => {
    rafQueue = rafQueue.filter(e => e.id !== id)
  })
  vi.stubGlobal('requestAnimationFrame', rafSpy)
  vi.stubGlobal('cancelAnimationFrame', cancelSpy)
})
afterEach(() => vi.unstubAllGlobals())

function flushRaf() {
  const q = rafQueue
  rafQueue = []
  q.forEach(e => e.cb(0))
}

function prepOf(seqs: bigint[], rowPx = 100): PreparedGeometry {
  const items: VirtualItem[] = seqs.map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
  return prepareGeometry({ items, offsetOfIndex: i => i * rowPx, totalHeight: seqs.length * rowPx })
}

/** A rail-relative rect: top 0, height 400 (so clientY maps 1:1 to a [0,1] fraction /400). */
function makeRect(): DOMRect {
  return { top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) } as DOMRect
}

function setup() {
  const el = document.createElement('div')
  el.setPointerCapture = vi.fn()
  el.releasePointerCapture = vi.fn()
  // Loaded window = seqs 1..5 over rows of 100px; whole range also 1..5 for the base case.
  const state = {
    minSeq: 1n,
    maxSeq: 5n,
    windowFirstSeq: 1n as bigint | undefined,
    windowLastSeq: 5n as bigint | undefined,
    prepared: prepOf([1n, 2n, 3n, 4n, 5n]),
    thumbHeightPx: 0, // 0 -> the centre axis is the full rail, so clientY/rect.height == fraction
  }
  // The resting thumb top at grab. The tests grab AT this position (clientY 100 == grabThumbTopPx),
  // so the within-thumb offset is 0 and the drag maps clientY/400 straight to the fraction -- the
  // offset-preservation itself is exercised by its own test below (a grab OFF the resting position).
  const grabThumbTopPx = 100
  const setDrag = vi.fn()
  const previewScrollTo = vi.fn()
  const onRelease = vi.fn()
  const onEnd = vi.fn()
  const deps: ThumbDragDeps = {
    el,
    rect: makeRect(),
    grabThumbTopPx,
    minSeq: () => state.minSeq,
    maxSeq: () => state.maxSeq,
    windowFirstSeq: () => state.windowFirstSeq,
    windowLastSeq: () => state.windowLastSeq,
    prepared: () => state.prepared,
    thumbHeightPx: () => state.thumbHeightPx,
    setDrag,
    previewScrollTo,
    onRelease,
    onEnd,
  }
  const handle = createThumbDrag(deps)
  return { el, handle, state, setDrag, previewScrollTo, onRelease, onEnd }
}

function move(el: HTMLElement, clientY: number) {
  el.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY }))
}
function up(el: HTMLElement, clientY: number) {
  el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY }))
}

describe('createthumbdrag', () => {
  it('captures the pointer and applies the initial position, live-scrolling in-window', () => {
    const { el, handle, setDrag, previewScrollTo } = setup()
    handle.start(7, 100) // clientY 100 of 400 -> fraction 0.25 -> seqF 1 + 0.25*4 = 2 (in window)
    expect(el.setPointerCapture).toHaveBeenCalledWith(7)
    expect(setDrag).toHaveBeenLastCalledWith(0.25)
    // seqF 2 -> contentY = top of row index 1 = 100px.
    expect(previewScrollTo).toHaveBeenCalledWith(100)
  })

  it('drops the drag cleanly when pointer capture fails', () => {
    const { el, handle, setDrag, onEnd } = setup()
    el.setPointerCapture = vi.fn(() => {
      throw new DOMException('pointer is no longer active', 'NotFoundError')
    })

    expect(() => handle.start(7, 100)).not.toThrow()
    expect(onEnd).toHaveBeenCalledTimes(1)
    expect(setDrag).toHaveBeenLastCalledWith(null)

    setDrag.mockClear()
    move(el, 200)
    flushRaf()
    expect(setDrag).not.toHaveBeenCalled()
  })

  it('maps the pointer onto the thumb-CENTRE axis when the thumb is inset', () => {
    const { el, handle, state, setDrag } = setup()
    state.thumbHeightPx = 200 // centre axis [100, 300] over the 400px rail (travel 200)
    handle.start(1, 200) // the axis midpoint -> fraction 0.5 (the thumb centre follows the pointer)
    expect(setDrag).toHaveBeenLastCalledWith(0.5)
    move(el, 50) // above the centre travel -> clamped to 0
    flushRaf()
    expect(setDrag).toHaveBeenLastCalledWith(0)
    move(el, 350) // below the centre travel -> clamped to 1
    flushRaf()
    expect(setDrag).toHaveBeenLastCalledWith(1)
  })

  it('holds the within-thumb grab offset -- no jump-on-grab when grabbing off the thumb centre', () => {
    const { el, handle, state, setDrag } = setup()
    // Resting thumb spans [100, 300] (grabThumbTopPx 100, height 200) -> resting fraction 0.5.
    state.thumbHeightPx = 200
    // Grab near the thumb's TOP edge (clientY 120), well off its centre (200). The OLD absolute
    // mapping snapped the thumb centre onto the cursor -> fraction 0.1 (a visible jump); the
    // offset-preserving drag keeps the thumb at its resting fraction and only tracks FROM there.
    handle.start(1, 120)
    expect(setDrag).toHaveBeenLastCalledWith(0.5) // no jump-on-grab
    setDrag.mockClear()
    // Moving the pointer down 40px moves the thumb 40px of its 200px centre-travel = +0.2.
    move(el, 160)
    flushRaf()
    expect(setDrag).toHaveBeenLastCalledWith(0.7)
  })

  it('previews the thumb but does NOT live-scroll when the drag maps outside the loaded window', () => {
    const { handle, state, setDrag, previewScrollTo } = setup()
    state.windowFirstSeq = 3n // the loaded window now starts above seqF 2
    handle.start(1, 100)
    expect(setDrag).toHaveBeenLastCalledWith(0.25)
    expect(previewScrollTo).not.toHaveBeenCalled()
  })

  it('previews the thumb but does NOT live-scroll when seq comparisons exceed safe numbers', () => {
    const { handle, state, setDrag, previewScrollTo } = setup()
    const unsafeBase = BigInt(Number.MAX_SAFE_INTEGER) + 1n
    state.minSeq = unsafeBase
    state.maxSeq = unsafeBase + 4n
    state.windowFirstSeq = unsafeBase
    state.windowLastSeq = unsafeBase + 4n
    state.prepared = prepOf([unsafeBase, unsafeBase + 1n, unsafeBase + 2n, unsafeBase + 3n, unsafeBase + 4n])

    handle.start(1, 100)

    expect(setDrag).toHaveBeenLastCalledWith(0.25)
    expect(previewScrollTo).not.toHaveBeenCalled()
  })

  it('coalesces rAF-throttled moves to the latest position', () => {
    const { el, handle, setDrag, previewScrollTo } = setup()
    handle.start(1, 100)
    setDrag.mockClear()
    previewScrollTo.mockClear()
    rafSpy.mockClear()
    move(el, 150)
    move(el, 200) // two moves before a frame -> a single scheduled rAF
    expect(rafSpy).toHaveBeenCalledTimes(1)
    expect(setDrag).not.toHaveBeenCalled() // nothing applied until the frame runs
    flushRaf()
    expect(setDrag).toHaveBeenCalledTimes(1)
    expect(setDrag).toHaveBeenLastCalledWith(0.5) // 200/400, the latest Y
    expect(previewScrollTo).toHaveBeenCalledWith(200) // seqF 3 -> row index 2 top = 200px
  })

  it('reports the release fraction on pointerup, stops tracking, and leaves the preview to the owner', () => {
    const { el, handle, setDrag, onRelease } = setup()
    handle.start(1, 100)
    setDrag.mockClear()
    up(el, 100) // fraction 0.25
    expect(onRelease).toHaveBeenCalledWith(0.25)
    // The controller does NOT clear the preview on release -- the owner holds it until settle.
    expect(setDrag).not.toHaveBeenCalled()
    // Listeners detached: a later move does nothing.
    move(el, 300)
    flushRaf()
    expect(setDrag).not.toHaveBeenCalled()
  })

  it('reports the fraction of the RELEASE position, not the grab', () => {
    const { el, handle, onRelease } = setup()
    handle.start(1, 100) // grabbed at 0.25
    up(el, 300) // released at 300/400 = 0.75
    expect(onRelease).toHaveBeenCalledWith(0.75)
  })

  it('abandons the drag on pointercancel: clears the preview and does not release/seek', () => {
    const { el, handle, setDrag, onRelease } = setup()
    handle.start(1, 100)
    setDrag.mockClear()
    // A system/edge gesture stole the pointer mid-drag.
    el.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, clientY: 200 }))
    expect(onRelease).not.toHaveBeenCalled() // an abort is not a seek
    expect(setDrag).toHaveBeenLastCalledWith(null) // preview dropped
    expect(el.releasePointerCapture).toHaveBeenCalledWith(1)
    // Detached: a later move does nothing.
    setDrag.mockClear()
    move(el, 300)
    flushRaf()
    expect(setDrag).not.toHaveBeenCalled()
  })

  it('cancel() clears the preview, releases capture, and stops tracking, cancelling a pending frame', () => {
    const { el, handle, setDrag, onRelease } = setup()
    handle.start(1, 100)
    move(el, 200) // schedules a frame
    setDrag.mockClear()
    handle.cancel()
    expect(cancelSpy).toHaveBeenCalled() // the pending rAF is cancelled
    expect(setDrag).toHaveBeenLastCalledWith(null)
    expect(el.releasePointerCapture).toHaveBeenCalledWith(1)
    expect(onRelease).not.toHaveBeenCalled() // a cancel is not a release
    // Fully detached: a later move + frame does nothing.
    setDrag.mockClear()
    move(el, 300)
    flushRaf()
    expect(setDrag).not.toHaveBeenCalled()
  })

  it('is idempotent: cancel() after a completed release is a harmless no-op', () => {
    const { el, handle, setDrag, onRelease } = setup()
    handle.start(1, 100)
    up(el, 100)
    onRelease.mockClear()
    setDrag.mockClear()
    expect(() => handle.cancel()).not.toThrow()
    expect(onRelease).not.toHaveBeenCalled()
    // cancel still clears the preview; harmless.
    expect(setDrag).toHaveBeenCalledWith(null)
  })

  it('fires onEnd exactly once per drag -- before onRelease on a deliberate release', () => {
    const { el, handle, onRelease, onEnd } = setup()
    handle.start(1, 100)
    expect(onEnd).not.toHaveBeenCalled() // still tracking
    up(el, 100)
    expect(onEnd).toHaveBeenCalledTimes(1)
    // onEnd frees the "drag active" guard before onRelease starts the seek.
    expect(onEnd.mock.invocationCallOrder[0]).toBeLessThan(onRelease.mock.invocationCallOrder[0])
    // A later cancel() (unmount) must NOT fire onEnd again -- the guard clears only once.
    handle.cancel()
    expect(onEnd).toHaveBeenCalledTimes(1)
  })

  it('fires onEnd once on pointercancel and on a bare cancel()', () => {
    const a = setup()
    a.handle.start(1, 100)
    a.el.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, clientY: 200 }))
    expect(a.onEnd).toHaveBeenCalledTimes(1)

    const b = setup()
    b.handle.start(1, 100)
    b.handle.cancel()
    expect(b.onEnd).toHaveBeenCalledTimes(1)
  })
})
