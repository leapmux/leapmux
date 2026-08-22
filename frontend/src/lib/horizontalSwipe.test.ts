import type { SwipeDirection } from './horizontalSwipe'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pointerEvent } from '~/test-support/pointer'
import { attachHorizontalSwipe, AXIS_LOCK_PX, SWIPE_MIN_PX } from './horizontalSwipe'

/**
 * The region the gesture is attached to, and the row inside it that every press
 * lands on. Pressing the CHILD is the realistic shape: the recognizer's guards
 * all walk up from the press target, and a press on the region itself would
 * skip every one of them.
 */
interface Harness {
  root: HTMLElement
  row: HTMLElement
  swipes: SwipeDirection[]
  detach: () => void
}

let harness: Harness | null = null

function mount(opts: { row?: HTMLElement } = {}): Harness {
  const root = document.createElement('div')
  const row = opts.row ?? document.createElement('div')
  root.appendChild(row)
  document.body.appendChild(root)
  const swipes: SwipeDirection[] = []
  const detach = attachHorizontalSwipe(root, { onSwipe: d => swipes.push(d) })
  harness = { root, row, swipes, detach }
  return harness
}

/** Give an element a sideways scroll range, which jsdom does not lay out. */
function makeSidewaysScroller(el: HTMLElement, opts: { scrollLeft: number, max: number }) {
  el.setAttribute('style', 'overflow-x: auto')
  Object.defineProperty(el, 'clientWidth', { value: 100, configurable: true })
  Object.defineProperty(el, 'scrollWidth', { value: 100 + opts.max, configurable: true })
  Object.defineProperty(el, 'scrollLeft', { value: opts.scrollLeft, configurable: true, writable: true })
}

/**
 * Dispatch a `touchmove` and report whether the gesture refused it. A plain
 * `Event` carries everything the handler reads (`cancelable`), and jsdom builds
 * no `TouchEvent` with real touch points.
 */
function touchMoveWasRefused(target: HTMLElement): boolean {
  const event = new Event('touchmove', { bubbles: true, cancelable: true })
  target.dispatchEvent(event)
  return event.defaultPrevented
}

/** Press, travel through `path`, and lift at the last point. */
function swipe(target: HTMLElement, path: Array<{ x: number, y: number }>) {
  const start = path[0]
  const end = path[path.length - 1]
  target.dispatchEvent(pointerEvent('pointerdown', { ...start, pointerType: 'touch' }))
  for (const point of path.slice(1))
    target.dispatchEvent(pointerEvent('pointermove', { ...point, pointerType: 'touch' }))
  target.dispatchEvent(pointerEvent('pointerup', { ...end, pointerType: 'touch' }))
}

/** A straight swipe from x=200 to `x`, sampled so it crosses the axis lock. */
function swipeTo(target: HTMLElement, x: number, y = 300) {
  const from = 200
  const step = (x - from) / 4
  swipe(target, [0, 1, 2, 3, 4].map(i => ({ x: from + step * i, y })))
}

beforeEach(() => {
  harness = null
})

afterEach(() => {
  harness?.detach()
  harness?.root.remove()
  harness = null
  // The selection is DOCUMENT state and outlives the fixture. A case that leaves
  // one behind would make the next one's press look like a reach for it.
  window.getSelection()?.removeAllRanges()
  document.body.innerHTML = ''
})

describe('attachHorizontalSwipe', () => {
  it('reports a rightward swipe past the travel threshold', () => {
    const h = mount()
    swipeTo(h.row, 200 + SWIPE_MIN_PX + 10)
    expect(h.swipes).toEqual(['right'])
  })

  it('reports a leftward swipe past the travel threshold', () => {
    const h = mount()
    swipeTo(h.row, 200 - SWIPE_MIN_PX - 10)
    expect(h.swipes).toEqual(['left'])
  })

  it('reports nothing for travel that stops short of the threshold', () => {
    const h = mount()
    // Past the axis lock, so the gesture IS live — and still under the travel
    // the release must hold.
    swipeTo(h.row, 200 + SWIPE_MIN_PX - 1)
    expect(h.swipes).toEqual([])
  })

  // The report lands under the finger, so a long drag must not report again
  // every few pixels, and the return leg of an overshoot must not report the
  // opposite swipe.
  it('reports once for one gesture, however far the finger goes on', () => {
    const h = mount()
    swipe(h.row, [
      { x: 200, y: 300 },
      { x: 200 + SWIPE_MIN_PX + 10, y: 300 },
      { x: 200 + SWIPE_MIN_PX + 120, y: 300 },
      { x: 100, y: 300 },
    ])
    expect(h.swipes).toEqual(['right'])
  })

  it('reports again for the next gesture', () => {
    const h = mount()
    swipeTo(h.row, 200 + SWIPE_MIN_PX + 10)
    swipeTo(h.row, 200 - SWIPE_MIN_PX - 10)
    expect(h.swipes).toEqual(['right', 'left'])
  })

  // A drag that goes down the page is the scroller's, however far sideways it
  // also drifts. The axis lock decides once, at the first travel past its
  // threshold, so a later sideways run cannot take the press back.
  it('reports nothing for a drag whose first travel is vertical', () => {
    const h = mount()
    swipe(h.row, [
      { x: 200, y: 300 },
      { x: 204, y: 300 + AXIS_LOCK_PX + 4 },
      { x: 200 + SWIPE_MIN_PX + 40, y: 300 + AXIS_LOCK_PX + 4 },
    ])
    expect(h.swipes).toEqual([])
  })

  it('reports nothing for a mouse drag', () => {
    const h = mount()
    const path = [{ x: 200, y: 300 }, { x: 320, y: 300 }]
    h.row.dispatchEvent(pointerEvent('pointerdown', { ...path[0] }))
    h.row.dispatchEvent(pointerEvent('pointermove', { ...path[1] }))
    h.row.dispatchEvent(pointerEvent('pointerup', { ...path[1] }))
    expect(h.swipes).toEqual([])
  })

  // A cancel before the swipe reports ends it. One that arrives after does not
  // take the report back — by then the drawer is already on screen.
  it('abandons a gesture the browser cancels before it reports', () => {
    const h = mount()
    h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
    // Locked horizontal, and still short of the travel that reports.
    h.row.dispatchEvent(pointerEvent('pointermove', { x: 230, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointercancel', { x: 230, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointermove', { x: 340, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointerup', { x: 340, y: 300, pointerType: 'touch' }))
    expect(h.swipes).toEqual([])
  })

  // A second finger is the start of a pinch, never a swipe.
  it('drops the gesture when a second finger lands', () => {
    const h = mount()
    h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointermove', { x: 240, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointerdown', { x: 100, y: 300, pointerType: 'touch', pointerId: 2, isPrimary: false }))
    h.row.dispatchEvent(pointerEvent('pointerup', { x: 320, y: 300, pointerType: 'touch' }))
    expect(h.swipes).toEqual([])
  })

  it('ignores travel that belongs to another pointer', () => {
    const h = mount()
    h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
    h.row.dispatchEvent(pointerEvent('pointermove', { x: 320, y: 300, pointerType: 'touch', pointerId: 7 }))
    h.row.dispatchEvent(pointerEvent('pointerup', { x: 320, y: 300, pointerType: 'touch', pointerId: 7 }))
    expect(h.swipes).toEqual([])
  })

  describe('presses that belong to someone else', () => {
    it.each([
      ['an input', () => document.createElement('input')],
      ['a textarea', () => document.createElement('textarea')],
      ['an editing host', () => {
        const el = document.createElement('div')
        el.setAttribute('contenteditable', 'true')
        return el
      }],
      ['a popover', () => {
        const el = document.createElement('div')
        el.setAttribute('popover', '')
        return el
      }],
      ['a region that declares touch-action', () => {
        const el = document.createElement('div')
        el.setAttribute('style', 'touch-action: none')
        return el
      }],
    ])('declines a press inside %s', (_label, build) => {
      const owner = build()
      const inner = document.createElement('span')
      owner.appendChild(inner)
      const h = mount({ row: owner })
      swipeTo(inner, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual([])
    })

    /**
     * A live selection owns every finger on the region until it is gone.
     *
     * Widening one means dragging the platform's own handles, and that drag is
     * HORIZONTAL along the line -- the same shape this recognizer reads as a
     * swipe. See ~/lib/tapSelect.ts, which is how a finger makes a selection.
     */
    describe('while the region holds a live selection', () => {
      function selectInside(el: HTMLElement) {
        el.textContent = 'a message body'
        const range = document.createRange()
        range.selectNodeContents(el)
        const selection = window.getSelection()!
        selection.removeAllRanges()
        selection.addRange(range)
      }

      it('declines the press', () => {
        const owner = document.createElement('div')
        const h = mount({ row: owner })
        selectInside(owner)
        swipeTo(owner, 200 + SWIPE_MIN_PX + 40)
        expect(h.swipes).toEqual([])
      })

      it('swipes again once the selection is gone', () => {
        const owner = document.createElement('div')
        const h = mount({ row: owner })
        selectInside(owner)
        swipeTo(owner, 200 + SWIPE_MIN_PX + 40)
        expect(h.swipes).toEqual([])

        window.getSelection()?.removeAllRanges()
        swipeTo(owner, 200 + SWIPE_MIN_PX + 40)
        expect(h.swipes).toEqual(['right'])
      })

      // A selection somewhere else on the page is not this region's business.
      it('takes the press when the selection is outside the region', () => {
        const outside = document.createElement('p')
        outside.textContent = 'elsewhere entirely'
        document.body.append(outside)
        const h = mount()
        const range = document.createRange()
        range.selectNodeContents(outside)
        const selection = window.getSelection()!
        selection.removeAllRanges()
        selection.addRange(range)

        swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
        expect(h.swipes).toEqual(['right'])
      })
    })

    it('takes a press on a plain row that declares nothing', () => {
      const owner = document.createElement('div')
      const inner = document.createElement('span')
      owner.appendChild(inner)
      const h = mount({ row: owner })
      swipeTo(inner, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
    })

    // The walk stops at the region, so a press on the region itself passes
    // every guard without examining anything.
    it('takes a press on the region itself', () => {
      const h = mount()
      swipeTo(h.root, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
    })

    it('declines a press from a button other than the contact', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch', button: 2 }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 320, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointerup', { x: 320, y: 300, pointerType: 'touch' }))
      expect(h.swipes).toEqual([])
    })
  })

  describe('sideways scrollers under the finger', () => {
    it('yields to a scroller that can still move the way the finger goes', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 40, max: 200 })
      const h = mount({ row })
      // Rightward: the block has 40px hidden to its left, so it consumes this.
      swipeTo(row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual([])
    })

    it('takes the swipe once the scroller has reached that end', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 0, max: 200 })
      const h = mount({ row })
      // Rightward with nothing hidden to the left: the block cannot use it.
      swipeTo(row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
      // ...and the other way it still can, so that one is the block's.
      swipeTo(row, 200 - SWIPE_MIN_PX - 40)
      expect(h.swipes).toEqual(['right'])
    })

    // The realistic shape: the finger lands on the text INSIDE a code block,
    // never on the scroll box itself. The walk up from the press target is
    // what finds the block at all.
    it('finds a scroller the press landed inside', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 40, max: 200 })
      const code = document.createElement('code')
      row.appendChild(code)
      const h = mount({ row })
      swipeTo(code, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual([])
    })

    it('yields to an overflow-x: scroll box too, not only an auto one', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 40, max: 200 })
      row.setAttribute('style', 'overflow-x: scroll')
      const h = mount({ row })
      swipeTo(row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual([])
    })

    // A sub-pixel overflow is rounding, not a scroller. Reading it as one
    // would make the gesture dead over any box whose content rounds up.
    it('ignores an overflow too small to scroll', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 1, max: 1 })
      const h = mount({ row })
      swipeTo(row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
    })

    it('ignores a wide element that does not scroll', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 40, max: 200 })
      // Wider than its box, but `overflow-x: visible` — it never scrolls, so
      // it has no claim on the press.
      row.setAttribute('style', 'overflow-x: visible')
      const h = mount({ row })
      swipeTo(row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
    })
  })

  /**
   * The half of the gesture that stops Blink from taking the finger away. See
   * the module doc: without it the engine starts a scroll on the first move,
   * fires `pointercancel`, and dispatches nothing else for that finger.
   */
  describe('refusing the browser scroll', () => {
    it('refuses a move once the axis has locked horizontal', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      // Before the lock the move could still become a scroll, so it is left alone.
      expect(touchMoveWasRefused(h.row)).toBe(false)

      h.row.dispatchEvent(pointerEvent('pointermove', { x: 220, y: 300, pointerType: 'touch' }))
      expect(touchMoveWasRefused(h.row)).toBe(true)
    })

    it('leaves a vertical drag to the scroller', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 202, y: 340, pointerType: 'touch' }))
      expect(touchMoveWasRefused(h.row)).toBe(false)
    })

    // The one that keeps a wide code block pannable: the gesture declines the
    // press, so it never locks, so it never refuses the block's own scroll.
    it('leaves a sideways scroller that wants the finger alone', () => {
      const row = document.createElement('div')
      makeSidewaysScroller(row, { scrollLeft: 40, max: 200 })
      mount({ row })
      row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      row.dispatchEvent(pointerEvent('pointermove', { x: 240, y: 300, pointerType: 'touch' }))
      expect(touchMoveWasRefused(row)).toBe(false)
    })

    it('gives the scroll back once the finger lifts', () => {
      const h = mount()
      swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
      expect(touchMoveWasRefused(h.row)).toBe(false)
    })

    it('stops refusing once detached', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 220, y: 300, pointerType: 'touch' }))
      h.detach()
      expect(touchMoveWasRefused(h.row)).toBe(false)
    })
  })

  describe('the click that trails the release', () => {
    it('swallows the click after a completed swipe', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(h.swipes).toEqual(['right'])
      expect(clicked).not.toHaveBeenCalled()
    })

    it('swallows one click only, so the next tap works', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(clicked).toHaveBeenCalledOnce()
    })

    // The report already happened, so the click it can synthesize is still
    // this gesture's to swallow — a cancel after the fact takes nothing back.
    it('still swallows the click when the browser cancels after the report', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 200 + SWIPE_MIN_PX + 40, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointercancel', { x: 200 + SWIPE_MIN_PX + 40, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(h.swipes).toEqual(['right'])
      expect(clicked).not.toHaveBeenCalled()
    })

    it('leaves the click alone when the cancel came before any report', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 230, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointercancel', { x: 230, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(clicked).toHaveBeenCalledOnce()
    })

    it('leaves a plain tap alone', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointerup', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(clicked).toHaveBeenCalledOnce()
    })

    it('still swallows the click when a second finger lands after the swipe reports', () => {
      const h = mount()
      const clicked = vi.fn()
      h.root.addEventListener('click', clicked)

      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      h.row.dispatchEvent(pointerEvent('pointermove', { x: 200 + SWIPE_MIN_PX + 40, y: 300, pointerType: 'touch' }))
      expect(h.swipes).toEqual(['right'])

      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 100, y: 300, pointerType: 'touch', pointerId: 2, isPrimary: false }))
      h.row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

      expect(clicked).not.toHaveBeenCalled()
    })
  })

  /**
   * The region is the whole mobile centre pane, so anything it takes it takes
   * from the transcript inside it. Capture would retarget every event after the
   * press AT this region, and the rows below would stop seeing `pointerup`
   * entirely -- which is what left a tapped message opening its context menu
   * half a second later. See `trackPress` for the measurement.
   */
  describe('what it must not take from the region below it', () => {
    it('never captures the pointer', () => {
      const h = mount()
      const capture = vi.fn()
      h.root.setPointerCapture = capture
      swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
      expect(h.swipes).toEqual(['right'])
      expect(capture).not.toHaveBeenCalled()
    })

    // The other half of the same decision: without capture the events have to
    // reach the recognizer some other way, and the document is that way.
    it('reads travel dispatched outside the region', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      document.body.dispatchEvent(pointerEvent('pointermove', { x: 200 + SWIPE_MIN_PX + 40, y: 300, pointerType: 'touch' }))
      expect(h.swipes).toEqual(['right'])
    })

    it('reads a release dispatched outside the region', () => {
      const h = mount()
      h.row.dispatchEvent(pointerEvent('pointerdown', { x: 200, y: 300, pointerType: 'touch' }))
      document.body.dispatchEvent(pointerEvent('pointerup', { x: 200, y: 300, pointerType: 'touch' }))
      // The release ended the gesture, so the travel that follows belongs to no
      // gesture and reports nothing.
      document.body.dispatchEvent(pointerEvent('pointermove', { x: 200 + SWIPE_MIN_PX + 40, y: 300, pointerType: 'touch' }))
      expect(h.swipes).toEqual([])
    })
  })

  it('stops reporting once detached', () => {
    const h = mount()
    h.detach()
    swipeTo(h.row, 200 + SWIPE_MIN_PX + 40)
    expect(h.swipes).toEqual([])
  })
})
