import type { TapSelectGranularity } from './tapSelect'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { inputOrEditableHosts, popoverHost } from '~/test-support/embeddedUi'
import { pointerEvent } from '~/test-support/pointer'
import { attachTapSelect, MULTI_TAP_MS, MULTI_TAP_RADIUS_PX } from './tapSelect'

const PARAGRAPH = 'the quick brown fox jumps over the lazy dog'
/** Where `brown` starts, so a case can aim at a word by name. */
const BROWN = PARAGRAPH.indexOf('brown')

/**
 * The layout jsdom does not have.
 *
 * `caretPositionFromPoint` needs a rendered box to hit-test, so jsdom implements
 * neither spelling of it and the gesture finds no caret at all there. This
 * supplies the one thing the environment is missing and nothing else: x IS the
 * character index in `node`. Everything downstream of the caret -- the word
 * boundaries, the paragraph boundaries, the range -- then runs for real against
 * real text, which is what these cases are about.
 */
function layoutOver(node: Text) {
  document.caretPositionFromPoint = ((x: number) => ({
    offsetNode: node,
    offset: Math.max(0, Math.min(Math.round(x), node.data.length)),
    // Part of the interface and read by nobody here. jsdom has no layout to
    // measure a caret against, and the gesture never asks for its rect.
    getClientRect: () => null,
  })) as unknown as typeof document.caretPositionFromPoint
}

describe('attachTapSelect', () => {
  let root: HTMLElement
  let para: HTMLParagraphElement
  let detach: () => void
  let onSelect: ReturnType<typeof vi.fn<(granularity: TapSelectGranularity) => void>>
  /** The clock `monotonicNow` reads, so a case can put a gap between two taps. */
  let clock: number

  beforeEach(() => {
    vi.useFakeTimers()
    clock = 1000
    vi.spyOn(performance, 'now').mockImplementation(() => clock)
    root = document.createElement('div')
    para = document.createElement('p')
    para.textContent = PARAGRAPH
    root.append(para)
    document.body.append(root)
    layoutOver(para.firstChild as Text)
    onSelect = vi.fn()
    detach = attachTapSelect(root, { onSelect })
  })

  afterEach(() => {
    detach()
    document.body.innerHTML = ''
    document.head.innerHTML = ''
    window.getSelection()?.removeAllRanges()
    Reflect.deleteProperty(document, 'caretPositionFromPoint')
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  interface TapOpts {
    /** Where the press lands. Defaults to the paragraph's own text. */
    on?: Element
    pointerType?: string
    isPrimary?: boolean
    /** Finger travel between the press and the release. */
    drift?: number
    /** How long the finger stays down. Advances the clock between the two events. */
    holdMs?: number
    /**
     * Where the rest of the press is dispatched, when it is not the press
     * target. This is what pointer capture on an ancestor does to every event
     * after the `pointerdown`.
     */
    releaseOn?: EventTarget
  }

  function tap(x: number, opts: TapOpts = {}) {
    const target = opts.on ?? para
    const rest = opts.releaseOn ?? target
    const shared = { pointerType: opts.pointerType ?? 'touch', isPrimary: opts.isPrimary, y: 0 }
    target.dispatchEvent(pointerEvent('pointerdown', { ...shared, x }))
    if (opts.drift !== undefined)
      rest.dispatchEvent(pointerEvent('pointermove', { ...shared, x: x + opts.drift }))
    clock += opts.holdMs ?? 0
    rest.dispatchEvent(pointerEvent('pointerup', { ...shared, x }))
  }

  function selected(): string {
    return window.getSelection()?.toString() ?? ''
  }

  describe('what a tap sequence selects', () => {
    it('selects the word under a double tap', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('brown')
    })

    it('widens the selection to the paragraph on a third tap', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe(PARAGRAPH)
    })

    it('keeps the paragraph on a fourth tap', () => {
      for (let i = 0; i < 4; i++)
        tap(BROWN + 2)
      expect(selected()).toBe(PARAGRAPH)
    })

    it('selects nothing on a single tap', () => {
      tap(BROWN + 2)
      expect(selected()).toBe('')
      expect(onSelect).not.toHaveBeenCalled()
    })

    it('reports the granularity of each selection', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(onSelect.mock.calls).toEqual([['word'], ['paragraph']])
    })

    it('starts a new sequence after the previous one finished', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('brown')

      clock += MULTI_TAP_MS + 1
      const lazy = PARAGRAPH.indexOf('lazy')
      tap(lazy + 1)
      expect(selected()).toBe('brown')
      tap(lazy + 1)
      expect(selected()).toBe('lazy')
    })
  })

  // Another gesture on an ANCESTOR can call `setPointerCapture`, and every event
  // after that `pointerdown` is then dispatched at the capturing element and
  // travels only its own ancestor chain -- so nothing inside this region sees the
  // rest of the press. The mobile drawer swipe captures to the centre pane above
  // the transcript (~/lib/horizontalSwipe.ts), which is exactly this shape.
  describe('a press whose later events are retargeted away', () => {
    let elsewhere: HTMLElement

    beforeEach(() => {
      elsewhere = document.createElement('div')
      document.body.append(elsewhere)
    })

    it('still counts the taps and selects', () => {
      tap(BROWN + 2, { releaseOn: elsewhere })
      tap(BROWN + 2, { releaseOn: elsewhere })
      expect(selected()).toBe('brown')
    })

    // Each of these ends the sequence, so its assertion is that NOTHING is
    // selected -- which a gesture that saw no events at all would satisfy too.
    // The clean pair afterwards is the control: it selects, so the gesture was
    // alive the whole time and the drift or the cancel is what stopped it.
    it('still sees the travel that ends a tap', () => {
      tap(BROWN + 2, { releaseOn: elsewhere })
      tap(BROWN + 2, { releaseOn: elsewhere, drift: PRESS_SLOP_PX + 5 })
      expect(selected()).toBe('')

      tap(BROWN + 2, { releaseOn: elsewhere })
      tap(BROWN + 2, { releaseOn: elsewhere })
      expect(selected()).toBe('brown')
    })

    it('still sees the cancel that ends a sequence', () => {
      tap(BROWN + 2, { releaseOn: elsewhere })
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      elsewhere.dispatchEvent(pointerEvent('pointercancel', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      tap(BROWN + 2, { releaseOn: elsewhere })
      expect(selected()).toBe('')

      tap(BROWN + 2, { releaseOn: elsewhere })
      expect(selected()).toBe('brown')
    })
  })

  describe('what ends a sequence before it selects', () => {
    it('ignores a mouse, which has the same two defaults already', () => {
      tap(BROWN + 2, { pointerType: 'mouse' })
      tap(BROWN + 2, { pointerType: 'mouse' })
      expect(selected()).toBe('')
    })

    it('ignores a second finger', () => {
      tap(BROWN + 2)
      tap(BROWN + 2, { isPrimary: false })
      expect(selected()).toBe('')
    })

    it('ends the sequence when a press travels past the slop', () => {
      tap(BROWN + 2)
      tap(BROWN + 2, { drift: PRESS_SLOP_PX + 5 })
      expect(selected()).toBe('')
      // ...and the drifting press is not the first tap of a new sequence either.
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })

    it('keeps a press that drifts inside the slop', () => {
      tap(BROWN + 2)
      tap(BROWN + 2, { drift: PRESS_SLOP_PX - 1 })
      expect(selected()).toBe('brown')
    })

    // Two long presses in a row would otherwise count as a double tap, and select
    // a word behind the message menu the second one opened.
    it('ends the sequence when a press is held long enough to be a long press', () => {
      tap(BROWN + 2)
      tap(BROWN + 2, { holdMs: motion.longPress })
      expect(selected()).toBe('')
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })

    it('keeps a press held just under the long-press threshold', () => {
      tap(BROWN + 2)
      tap(BROWN + 2, { holdMs: motion.longPress - 1 })
      expect(selected()).toBe('brown')
    })

    it('ends the sequence when the taps are too far apart in time', () => {
      tap(BROWN + 2)
      clock += MULTI_TAP_MS + 1
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })

    it('chains two taps inside the window', () => {
      tap(BROWN + 2)
      clock += MULTI_TAP_MS
      tap(BROWN + 2)
      expect(selected()).toBe('brown')
    })

    it('ends the sequence when the taps land too far apart', () => {
      tap(BROWN)
      tap(BROWN + MULTI_TAP_RADIUS_PX + 1)
      expect(selected()).toBe('')
    })

    it('ends the sequence when the browser cancels the touch', () => {
      tap(BROWN + 2)
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      para.dispatchEvent(pointerEvent('pointercancel', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })

    it('selects nothing where the platform reports no caret', () => {
      Reflect.deleteProperty(document, 'caretPositionFromPoint')
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('')
      expect(onSelect).not.toHaveBeenCalled()
    })

    it('selects nothing when the caret lands outside the region', () => {
      const outside = document.createElement('p')
      outside.textContent = 'somewhere else'
      document.body.append(outside)
      layoutOver(outside.firstChild as Text)
      tap(2)
      tap(2)
      expect(selected()).toBe('')
    })
  })

  /**
   * The two spellings of the caret hit-test, and the shape each can report.
   *
   * `caretPositionFromPoint` is the standard one and `caretRangeFromPoint` is
   * the older one WebKit shipped first, so a device running an older Safari
   * reaches the gesture only through the fallback. Neither is in jsdom, so a
   * case that installs one spelling and not the other is the only way to
   * exercise the choice at all.
   */
  describe('the caret the platform reports', () => {
    /** The WebKit spelling: a collapsed Range rather than a position. */
    function rangeSpellingOver(node: Text) {
      Reflect.deleteProperty(document, 'caretPositionFromPoint')
      document.caretRangeFromPoint = ((x: number) => {
        const range = document.createRange()
        range.setStart(node, Math.max(0, Math.min(Math.round(x), node.data.length)))
        range.collapse(true)
        return range
      }) as typeof document.caretRangeFromPoint
    }

    afterEach(() => {
      Reflect.deleteProperty(document, 'caretRangeFromPoint')
    })

    it('falls back to the older spelling when it is the only one', () => {
      rangeSpellingOver(para.firstChild as Text)
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('brown')
    })

    // The standard spelling wins where both exist, so one engine's quirks in
    // the legacy one cannot change what a tap selects.
    it('prefers the standard spelling when the platform has both', () => {
      const lazy = PARAGRAPH.indexOf('lazy')
      rangeSpellingOver(para.firstChild as Text)
      document.caretPositionFromPoint = (() => ({
        offsetNode: para.firstChild as Text,
        offset: lazy + 1,
        getClientRect: () => null,
      })) as unknown as typeof document.caretPositionFromPoint

      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('lazy')
    })

    /**
     * An engine reports the position inside an ELEMENT when the point falls
     * between that element's children -- in a paragraph's padding, or past the
     * end of its last line. `offset` is then a child index, so the gesture has
     * to walk to the text the element holds.
     */
    it('normalizes a caret the engine reports on an element', () => {
      document.caretPositionFromPoint = (() => ({
        offsetNode: para,
        offset: 0,
        getClientRect: () => null,
      })) as unknown as typeof document.caretPositionFromPoint

      tap(0)
      tap(0)
      expect(selected()).toBe('the')
    })

    it('selects nothing when the element it reports holds no text', () => {
      const empty = document.createElement('div')
      root.append(empty)
      document.caretPositionFromPoint = (() => ({
        offsetNode: empty,
        offset: 0,
        getClientRect: () => null,
      })) as unknown as typeof document.caretPositionFromPoint

      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })
  })

  describe('presses that belong to something else', () => {
    // The shared fragment every pointer guard in the app composes. A member
    // dropped from it must fail here as well as in the other guards' specs.
    it.each([
      ...inputOrEditableHosts(),
      popoverHost(),
      (() => {
        const host = document.createElement('button')
        host.textContent = 'Retry'
        return { label: '<button>', host, target: host }
      })(),
      (() => {
        const host = document.createElement('div')
        host.setAttribute('role', 'button')
        host.textContent = 'Retry'
        return { label: '[role="button"]', host, target: host }
      })(),
      (() => {
        const host = document.createElement('a')
        host.href = '#somewhere'
        host.textContent = 'a link'
        return { label: 'a[href]', host, target: host }
      })(),
      (() => {
        const host = document.createElement('details')
        const inner = document.createElement('summary')
        inner.textContent = 'Show more'
        host.append(inner)
        return { label: '<summary>', host, target: inner }
      })(),
      (() => {
        const host = document.createElement('div')
        host.setAttribute('data-no-tap-select', '')
        const inner = document.createElement('span')
        inner.textContent = 'Quote'
        host.append(inner)
        return { label: '[data-no-tap-select]', host, target: inner }
      })(),
    ])('declines a press inside $label', ({ host, target }) => {
      root.append(host)
      tap(BROWN + 2, { on: target })
      tap(BROWN + 2, { on: target })
      expect(selected()).toBe('')
    })

    it('ends an open sequence rather than interrupting it', () => {
      const button = document.createElement('button')
      root.append(button)
      tap(BROWN + 2)
      tap(BROWN + 2, { on: button })
      tap(BROWN + 2)
      expect(selected()).toBe('')
    })
  })

  describe('the user-select suppression it lifts', () => {
    let suppressed: HTMLElement

    beforeEach(() => {
      const style = document.createElement('style')
      style.textContent = '.suppressed { user-select: none; }'
      document.head.append(style)
      suppressed = document.createElement('div')
      suppressed.className = 'suppressed'
      root.append(suppressed)
      suppressed.append(para)
    })

    /** Collapse the selection the way the popover's Copy does, and report it. */
    function clearSelection() {
      window.getSelection()?.removeAllRanges()
      document.dispatchEvent(new Event('selectionchange'))
    }

    /** Give the live range geometry, so a press can land ON the highlight. */
    function highlightAt(rect: { left: number, right: number, top: number, bottom: number }) {
      const live = window.getSelection()!.getRangeAt(0) as Range & { getClientRects: () => DOMRectList }
      live.getClientRects = () => [rect] as unknown as DOMRectList
    }

    it('lifts the suppression on the element that declares it', () => {
      expect(getComputedStyle(suppressed).userSelect).toBe('none')
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(suppressed.style.userSelect).toBe('text')
      expect(suppressed.style.webkitUserSelect).toBe('text')
    })

    it('puts it back when the selection is cleared', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      clearSelection()
      expect(suppressed.style.userSelect).toBe('')
      expect(suppressed.style.webkitUserSelect).toBe('')
    })

    it('puts it back on the next press that starts something new', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      expect(suppressed.style.userSelect).toBe('')
    })

    /**
     * The selection goes with the lift, and the browser cannot do it for us.
     *
     * It collapses a selection when the next press places a caret, and it places
     * no caret in text it may not select -- so a lift put back on its own
     * strands the range. Measured in Chromium: `rangeCount` stayed 1 and
     * `isCollapsed` false after the press, with only `Selection.toString()`
     * reading empty. The message menu and the drawer swipe both stand aside for
     * a live selection, so a stranded one takes them away for good.
     */
    it('takes the selection away with it', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(window.getSelection()?.rangeCount).toBe(1)

      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      expect(window.getSelection()?.rangeCount).toBe(0)
    })

    // A selection the gesture did not have to lift for is the browser's to
    // manage: a file view has no suppression, and a mouse never comes here.
    it('leaves a selection it never lifted for alone', () => {
      suppressed.classList.remove('suppressed')
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(window.getSelection()?.toString()).toBe('brown')

      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: BROWN + 2, y: 0 }))
      expect(window.getSelection()?.toString()).toBe('brown')
    })

    it('keeps it lifted for a finger that lands on the highlight', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      highlightAt({ left: 0, right: 200, top: 0, bottom: 40 })
      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: 50, y: 20 }))
      expect(suppressed.style.userSelect).toBe('text')
    })

    // The platform draws its selection handles below the last line of the
    // highlight, so the press that reaches for one lands outside every rect the
    // selection reports. Dropping the lift there would end the selection the
    // finger came to adjust.
    it('keeps it lifted for a finger reaching just past the highlight', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      highlightAt({ left: 0, right: 200, top: 0, bottom: 40 })
      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: 50, y: 60 }))
      expect(suppressed.style.userSelect).toBe('text')
    })

    it('drops it for a finger well clear of the highlight', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      highlightAt({ left: 0, right: 200, top: 0, bottom: 40 })
      clock += MULTI_TAP_MS + 1
      para.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x: 50, y: 400 }))
      expect(suppressed.style.userSelect).toBe('')
    })

    it('puts it back on detach', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      detach()
      detach = () => {}
      expect(suppressed.style.userSelect).toBe('')
    })

    it('leaves nothing lifted when the attempt selects nothing', () => {
      Reflect.deleteProperty(document, 'caretPositionFromPoint')
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(suppressed.style.userSelect).toBe('')
    })

    it('restores what the element already declared inline', () => {
      suppressed.style.userSelect = 'none'
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(suppressed.style.userSelect).toBe('text')
      clearSelection()
      expect(suppressed.style.userSelect).toBe('none')
    })

    it('keeps the lift while the selection is still inside the region', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      document.dispatchEvent(new Event('selectionchange'))
      expect(suppressed.style.userSelect).toBe('text')
    })
  })

  describe('the mouse events a tap synthesizes', () => {
    function mousedown(): boolean {
      return !para.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    }

    it('refuses the default action of the one that follows a selection', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      // Unrefused, this collapses the selection at the caret a frame after the
      // gesture set it, and its own default action is the engine's word select.
      expect(mousedown()).toBe(true)
    })

    it('refuses only the first one', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      expect(mousedown()).toBe(true)
      expect(mousedown()).toBe(false)
    })

    it('stops refusing once the grace window passes with no mousedown', () => {
      tap(BROWN + 2)
      tap(BROWN + 2)
      vi.advanceTimersByTime(2000)
      expect(mousedown()).toBe(false)
    })

    it('refuses nothing when no selection was made', () => {
      tap(BROWN + 2)
      expect(mousedown()).toBe(false)
    })
  })

  it('stops selecting once detached', () => {
    detach()
    detach = () => {}
    tap(BROWN + 2)
    tap(BROWN + 2)
    expect(selected()).toBe('')
  })
})
