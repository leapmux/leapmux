import type { ThumbDragDeps } from './chatScrollRailDrag'
import type { PreparedGeometry } from './chatScrollRailGeometry'
import type { VirtualItem } from './useChatVirtualizer'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createThumbDrag, SCRUB_SEEK_DEBOUNCE_MS } from './chatScrollRailDrag'
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

function setup(opts: { engageSlopPx?: number } = {}) {
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
  const scrubSeek = vi.fn()
  const onEngage = vi.fn()
  const onRelease = vi.fn()
  const onEnd = vi.fn()
  const deps: ThumbDragDeps = {
    el,
    rect: makeRect(),
    grabThumbTopPx,
    // Default 0 -- the thumb-grab case, where the first pixel of travel scrubs. The slop tests
    // pass their own.
    engageSlopPx: opts.engageSlopPx ?? 0,
    minSeq: () => state.minSeq,
    maxSeq: () => state.maxSeq,
    windowFirstSeq: () => state.windowFirstSeq,
    windowLastSeq: () => state.windowLastSeq,
    prepared: () => state.prepared,
    thumbHeightPx: () => state.thumbHeightPx,
    setDrag,
    previewScrollTo,
    scrubSeek,
    onEngage,
    onRelease,
    onEnd,
  }
  const handle = createThumbDrag(deps)
  return { el, handle, state, setDrag, previewScrollTo, scrubSeek, onEngage, onRelease, onEnd }
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
    // Pressed and released on the same pixel: a TAP, so `engaged` is false and the owner keeps
    // whatever meaning it gave the press rather than seeking again.
    expect(onRelease).toHaveBeenCalledWith(0.25, false)
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
    expect(onRelease).toHaveBeenCalledWith(0.75, true)
  })

  it('engages off the RELEASE position when the browser coalesced the last move into pointerup', () => {
    // A pointerup can carry a position no pointermove ever reported. Trusting only the moves
    // would report this real drag as a tap and drop its seek.
    const { el, handle, onEngage, onRelease } = setup({ engageSlopPx: 6 })
    handle.start(1, 100)
    up(el, 300) // 200px of travel, none of it in a pointermove
    expect(onRelease).toHaveBeenCalledWith(0.75, true)
    // onEngage is for a LIVE scrub (it drops the press's own stale seek); a release already
    // supersedes that seek with its own, so the controller does not fire it here.
    expect(onEngage).not.toHaveBeenCalled()
  })

  describe('engage slop', () => {
    it('drops a move within the slop: no preview, no live-scroll, and the release is a tap', () => {
      const { el, handle, setDrag, previewScrollTo, onEngage, onRelease } = setup({ engageSlopPx: 6 })
      handle.start(1, 100)
      setDrag.mockClear()
      previewScrollTo.mockClear()
      move(el, 106) // exactly the slop -- still a tap
      flushRaf()
      expect(setDrag).not.toHaveBeenCalled()
      expect(previewScrollTo).not.toHaveBeenCalled()
      expect(onEngage).not.toHaveBeenCalled()
      // The tap reports the PRESS fraction (0.25), not the drifted release position, so the
      // owner's pinned thumb stays on the point the press acted on.
      up(el, 104)
      expect(onRelease).toHaveBeenCalledWith(0.25, false)
    })

    it('engages once past the slop, then tracks every later move', () => {
      const { el, handle, setDrag, onEngage, onRelease } = setup({ engageSlopPx: 6 })
      handle.start(1, 100)
      setDrag.mockClear()
      move(el, 107) // one past the slop
      flushRaf()
      expect(onEngage).toHaveBeenCalledTimes(1)
      expect(setDrag).toHaveBeenLastCalledWith(107 / 400)
      move(el, 300)
      flushRaf()
      expect(onEngage).toHaveBeenCalledTimes(1) // engaging is one-way
      expect(setDrag).toHaveBeenLastCalledWith(0.75)
      up(el, 300)
      expect(onRelease).toHaveBeenCalledWith(0.75, true)
    })

    it('engages on the first move with the default slop of 0 (the thumb grab)', () => {
      const { el, handle, onEngage, setDrag } = setup()
      handle.start(1, 100)
      setDrag.mockClear()
      move(el, 101) // a single pixel: a thumb must move with the pointer, never wait
      flushRaf()
      expect(onEngage).toHaveBeenCalledTimes(1)
      expect(setDrag).toHaveBeenLastCalledWith(101 / 400)
    })

    it('drags with the optional deps omitted entirely', () => {
      // engageSlopPx, scrubSeek and onEngage are all optional. With none of them supplied the
      // controller must still track and release -- the slop defaults to 0 and an out-of-window
      // rest simply seeks nothing, rather than calling through an undefined sink.
      const el = document.createElement('div')
      el.setPointerCapture = vi.fn()
      el.releasePointerCapture = vi.fn()
      const setDrag = vi.fn()
      const onRelease = vi.fn()
      const handle = createThumbDrag({
        el,
        rect: makeRect(),
        grabThumbTopPx: 100,
        minSeq: () => 1n,
        maxSeq: () => 5n,
        windowFirstSeq: () => undefined, // every target is out of window: the seek path is live
        windowLastSeq: () => undefined,
        prepared: () => prepOf([1n, 2n, 3n, 4n, 5n]),
        thumbHeightPx: () => 0,
        setDrag,
        previewScrollTo: vi.fn(),
        onRelease,
      })
      handle.start(1, 100)
      expect(() => {
        move(el, 200)
        flushRaf()
      }).not.toThrow()
      expect(setDrag).toHaveBeenLastCalledWith(0.5) // the default slop of 0 engaged on that move
      up(el, 200)
      expect(onRelease).toHaveBeenCalledWith(0.5, true)
    })
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

  describe('out-of-window settle seek', () => {
    // Only setTimeout/clearTimeout are faked: rAF stays the hand-driven stub above, which the
    // move coalescer needs.
    beforeEach(() => vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] }))
    afterEach(() => vi.useRealTimers())

    /** Move the thumb to an out-of-window position and flush the frame it schedules. */
    function scrubTo(el: HTMLElement, clientY: number) {
      move(el, clientY)
      flushRaf()
    }

    it('seeks the settled seq once the debounce elapses', () => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n // seqs below 4 are out of the loaded window
      handle.start(1, 100) // grabbed on the resting thumb top: clientY maps 1:1 to the fraction
      scrubTo(el, 200) // fraction 0.5 -> seq 3, out of window
      expect(scrubSeek).not.toHaveBeenCalled() // not until the thumb has rested
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).toHaveBeenCalledWith(3n)
    })

    it('never seeks an in-window target -- that path live-scrolls instead', () => {
      const { el, handle, scrubSeek, previewScrollTo } = setup()
      handle.start(1, 100)
      scrubTo(el, 200) // seqF 3, inside the loaded window 1..5
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS * 4)
      expect(scrubSeek).not.toHaveBeenCalled()
      expect(previewScrollTo).toHaveBeenCalledWith(200)
    })

    it('does not arm a seek from the press itself', () => {
      // The owner decides what a press means (a track/dot press jumps on its own). If the
      // initial apply armed one too, every out-of-window press would fetch its page twice.
      const { handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100) // out of window, but no move yet
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS * 4)
      expect(scrubSeek).not.toHaveBeenCalled()
    })

    it('restarts the debounce on every move: only the settled position seeks', () => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200) // seq 3
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS - 1)
      scrubTo(el, 100) // seq 2 -- still out of window, and it supersedes the pending seek
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).toHaveBeenCalledTimes(1)
      expect(scrubSeek).toHaveBeenCalledWith(2n)
    })

    it('drops a pending seek when the thumb scrubs back into the loaded window', () => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200) // seq 3, out of window: a seek is armed
      scrubTo(el, 300) // back in-window (seqF 4): the armed fetch is now for a stale position
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS * 4)
      expect(scrubSeek).not.toHaveBeenCalled()
    })

    it('re-tests the window when the timer fires, not only when it was armed', () => {
      // An earlier seek (or an edge page load) can swap the window during the wait, which makes
      // this target in-window and the fetch pointless.
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200)
      state.windowFirstSeq = 1n // the window swapped mid-wait: seq 3 is loaded now
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).not.toHaveBeenCalled()
    })

    it('seeks the same seq only once across two rests', () => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200)
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).toHaveBeenCalledTimes(1)
      // A jitter that maps back to the SAME seq (3) must not fetch its page again.
      scrubTo(el, 201)
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).toHaveBeenCalledTimes(1)
    })

    it.each([
      ['pointerup', (el: HTMLElement) => up(el, 100)],
      ['pointercancel', (el: HTMLElement) => el.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, clientY: 100 }))],
    ])('cancels a pending seek on %s', (_name, end) => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200)
      end(el)
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS * 4)
      // A release seeks through the owner's own release path; a late duplicate from this timer
      // would fetch a second page behind it.
      expect(scrubSeek).not.toHaveBeenCalled()
    })

    it('seeks when the loaded window is UNKNOWN, rather than scrubbing over a frozen transcript', () => {
      // No window bounds at all (a conversation whose rows are all optimistic locals, or a
      // window that has not resolved yet) is the fail-closed case: "outside". The reader must
      // still get the debounced seek, or the thumb moves over a transcript that never follows.
      const { el, handle, state, scrubSeek, previewScrollTo } = setup()
      state.windowFirstSeq = undefined
      state.windowLastSeq = undefined
      handle.start(1, 100)
      scrubTo(el, 200)
      expect(previewScrollTo).not.toHaveBeenCalled() // nothing loaded to live-scroll to
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).toHaveBeenCalledWith(3n)
    })

    it('seeks nothing when the seq range degenerates between the arm and the fire', () => {
      // The range is re-read when the timer fires, so a window that goes inverted mid-wait must
      // fail closed rather than map the fraction onto a range that no longer makes sense.
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200)
      state.maxSeq = 0n // inverted: maxSeq < minSeq
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
      expect(scrubSeek).not.toHaveBeenCalled()
    })

    it('cancels a pending seek on cancel() (an unmount mid-scrub)', () => {
      const { el, handle, state, scrubSeek } = setup()
      state.windowFirstSeq = 4n
      handle.start(1, 100)
      scrubTo(el, 200)
      handle.cancel()
      vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS * 4)
      expect(scrubSeek).not.toHaveBeenCalled()
    })
  })
})
