import type { ContextMenuPress } from './contextMenuGesture'
import type { PointerOpts } from '~/test-support/pointer'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { motion } from '~/styles/tokens'
import { inputOrEditableHosts, popoverHost } from '~/test-support/embeddedUi'
import { pointerEvent } from '~/test-support/pointer'
import { selectTextWithRect } from '~/test-support/selection'
import { attachContextMenuGesture, CLICK_AFTER_RELEASE_GRACE_MS, PRESS_SLOP_PX, pressAnchorRect, touchReleaseOpensMenu } from './contextMenuGesture'

const HOLD_MS = motion.longPress

/** Whether an open menu is currently refusing pointers to the holding finger. */
function heldOverMenu(): boolean {
  return document.documentElement.hasAttribute('data-ctx-hold-over-menu')
}

/** The gesture's own events are touch presses; the shared factory defaults to a mouse. */
function pointer(type: string, opts: PointerOpts = {}): PointerEvent {
  return pointerEvent(type, { pointerType: 'touch', ...opts })
}

describe('pressAnchorRect', () => {
  it('is a zero-height band at the press point', () => {
    // `calcPopoverPosition` puts the popover below `bottom` and left-aligned to
    // `left`, so collapsing the band onto the point lands the menu's corner there.
    expect(pressAnchorRect({ clientX: 150, clientY: 108 }))
      .toEqual({ top: 108, bottom: 108, left: 150 })
  })

  it('carries no finger-clearance offset for touch', () => {
    // The menu grows down and to the right of the press point, so the fingertip
    // covers a corner of it for the moment before the lift and nothing after.
    // One rule serves both inputs.
    expect(pressAnchorRect({ clientX: 40, clientY: 0 }))
      .toEqual({ top: 0, bottom: 0, left: 40 })
  })
})

describe('attachContextMenuGesture', () => {
  let row: HTMLElement
  let detach: () => void
  let onOpen: ReturnType<typeof vi.fn<(press: ContextMenuPress) => void>>
  /** The rect the row reports; a test moves it to make a scroll "move the row". */
  let rowRect: { top: number, left: number }

  beforeEach(() => {
    vi.useFakeTimers()
    row = document.createElement('div')
    rowRect = { top: 100, left: 40 }
    row.getBoundingClientRect = () => ({
      top: rowRect.top,
      bottom: rowRect.top + 22,
      left: rowRect.left,
      right: rowRect.left + 200,
      width: 200,
      height: 22,
      x: rowRect.left,
      y: rowRect.top,
      toJSON: () => ({}),
    })
    document.body.appendChild(row)
    onOpen = vi.fn()
    detach = attachContextMenuGesture(row, { onOpen })
  })

  afterEach(() => {
    detach()
    row.remove()
    vi.useRealTimers()
  })

  /**
   * Run the hold out and let its menu open.
   *
   * Two advances, because the open is one task behind the hold: the hold timer
   * fires at `HOLD_MS` and schedules the open, and a fake-timer advance does not
   * run a timer scheduled during the advance it is already inside.
   */
  function completeHold() {
    vi.advanceTimersByTime(HOLD_MS)
    vi.runOnlyPendingTimers()
  }

  /** Drive a full touch press-and-hold that completes, then release. */
  function holdAndRelease(x = 90, y = 110) {
    row.dispatchEvent(pointer('pointerdown', { x, y }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x, y }))
    vi.runAllTimers()
  }

  it('opens after the hold completes, anchored at the press x', () => {
    holdAndRelease(90, 110)

    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 90 }))
  })

  it('marks the row while the hold is in flight and clears it when the menu arrives', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    expect(row.hasAttribute('data-press-hold')).toBe(true)

    // The menu is the answer the tint was promising, so the tint stops here --
    // holding it at full under an open menu until the finger lifted said
    // nothing, twice.
    vi.advanceTimersByTime(HOLD_MS)
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  it('clears the tint on a release that never reached the hold', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS - 1)
    expect(row.hasAttribute('data-press-hold')).toBe(true)

    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  // The menu opens under the finger that is still pressing, and a held touch
  // carries a hover with it -- so an item sat there looking chosen. The
  // document-level marker is what the CSS takes hit-testing away on.
  it('keeps an open menu inert until the click behind the release has landed', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    expect(heldOverMenu()).toBe(false)

    vi.advanceTimersByTime(HOLD_MS)
    expect(heldOverMenu()).toBe(true)

    // The LIFT does not hand the menu back. The click it synthesizes has not
    // been built yet, and a menu made solid again here catches it: the release
    // then activated whichever item the finger came down on, which ran that
    // item's action and closed the menu -- read from the outside as "the menu
    // is dismissed when I let go".
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    expect(heldOverMenu()).toBe(true)

    // Seeing the click is enough: its target was decided when the browser built
    // it, so by now it belongs to the row under the transparent menu.
    row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(heldOverMenu()).toBe(false)
  })

  it('ends the inert window on its own when no click follows the release', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    expect(heldOverMenu()).toBe(true)

    // A cancelled touch, or an engine that suppresses the click after a long
    // press, would otherwise leave the menu unable to take a tap at all.
    vi.advanceTimersByTime(CLICK_AFTER_RELEASE_GRACE_MS)
    expect(heldOverMenu()).toBe(false)
  })

  it('arms the indicator as an attribute that survives a class-list rewrite', () => {
    // The rows that mount the gesture assign their `class` reactively, and that
    // assignment replaces the whole class list -- a Solid `class={...}` template
    // string runs `element.className = value` on every change (a tab becoming
    // active, a chat row reclassifying mid-stream). The indicator is an
    // attribute precisely so it survives this.
    expect(row.getAttribute('data-ctx-menu')).toBe('owned')

    row.className = 'row-active row-dragging'
    expect(row.getAttribute('data-ctx-menu')).toBe('owned')
  })

  // The hold IS the confirmation, so the menu belongs to it and not to the lift.
  it('opens while the finger is still down, from the hold and not a pointer handler', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    // Nothing from the press itself: the open arrives on the hold's own timer.
    expect(onOpen).not.toHaveBeenCalled()

    completeHold()
    expect(onOpen).toHaveBeenCalledTimes(1)

    // The lift adds nothing where the platform left that menu alone.
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('opens once for a whole gesture', () => {
    holdAndRelease()
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('does nothing when the finger lifts before the threshold', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS - 1)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  it('lets the row click through when the hold did not complete', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS - 1)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))

    const click = new MouseEvent('click', { bubbles: true, cancelable: true })
    row.dispatchEvent(click)

    expect(click.defaultPrevented).toBe(false)
  })

  it('swallows the click that completes a fired hold', () => {
    const documentClick = vi.fn()
    document.addEventListener('click', documentClick)

    holdAndRelease()
    const click = new MouseEvent('click', { bubbles: true, cancelable: true })
    row.dispatchEvent(click)

    expect(click.defaultPrevented).toBe(true)
    expect(documentClick).not.toHaveBeenCalled()

    document.removeEventListener('click', documentClick)
  })

  it('swallows only the one click it claimed', () => {
    holdAndRelease()
    row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))

    const later = new MouseEvent('click', { bubbles: true, cancelable: true })
    row.dispatchEvent(later)

    expect(later.defaultPrevented).toBe(false)
  })

  it('cancels when the finger travels past the slop', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    row.dispatchEvent(pointer('pointermove', { x: 90, y: 110 + PRESS_SLOP_PX + 1 }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 + PRESS_SLOP_PX + 1 }))
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  it('tolerates travel within the slop', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    row.dispatchEvent(pointer('pointermove', { x: 90, y: 110 + PRESS_SLOP_PX - 1 }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('cancels on pointercancel, which is the browser claiming the touch for a pan', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    row.dispatchEvent(pointer('pointercancel'))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  // The hold's menu is already up by the time a pan can claim the touch, so a
  // cancel can no longer un-open it -- what it still does is stop the gesture
  // from asserting that menu again. Before the menu moved to the hold, this
  // dropped the open outright.
  it('opens once on the hold and not again when a pointercancel follows', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()
    expect(onOpen).toHaveBeenCalledTimes(1)

    row.dispatchEvent(pointer('pointercancel'))
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('cancels when a scroll moves the row', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    // The row's ancestor pans: the row itself moves in the viewport.
    rowRect.top += 40
    document.dispatchEvent(new Event('scroll'))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
  })

  it('keeps the hold through a scroll that does not move the row', () => {
    // The app scrolls itself constantly -- the chat list sticks to the bottom
    // while the agent streams -- and none of those scrolls touches this row.
    // Cancelling on any scroll would make a long press impossible while
    // anything streams.
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    document.dispatchEvent(new Event('scroll'))
    document.dispatchEvent(new Event('scroll'))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('does not re-assert a held-open menu when a scroll moves the row', () => {
    // On engines that pan without `pointercancel`, the scroll fallback is the
    // only cancel. It can no longer drop the menu -- the hold opened that
    // already -- so what it drops is the release's re-assert.
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()
    expect(onOpen).toHaveBeenCalledTimes(1)

    rowRect.top += 40
    document.dispatchEvent(new Event('scroll'))
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('ignores a scroll once no hold is in flight', () => {
    // Nothing is armed, so the listener must already be detached and this must not
    // reach into the gesture's state.
    document.dispatchEvent(new Event('scroll'))
    holdAndRelease()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('never starts a hold for a mouse', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110, pointerType: 'mouse' }))
    expect(row.hasAttribute('data-press-hold')).toBe(false)

    vi.advanceTimersByTime(HOLD_MS * 2)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
  })

  it('ignores a secondary finger', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110, isPrimary: false }))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
  })

  it('ignores a press that starts inside a text input', () => {
    const input = document.createElement('input')
    row.appendChild(input)

    input.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
    expect(row.hasAttribute('data-press-hold')).toBe(false)
  })

  it('ignores a press that starts inside any editable host', () => {
    // A long press inside an editor must keep the native selection handles
    // and the paste callout, whichever spelling the host carries.
    for (const spelling of ['true', '', 'plaintext-only']) {
      const editor = document.createElement('div')
      editor.setAttribute('contenteditable', spelling)
      row.appendChild(editor)

      editor.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      vi.runAllTimers()

      expect(onOpen, `contenteditable="${spelling}"`).not.toHaveBeenCalled()
      expect(row.hasAttribute('data-press-hold')).toBe(false)

      const e = new MouseEvent('contextmenu', { clientX: 95, bubbles: true, cancelable: true })
      editor.dispatchEvent(e)
      expect(e.defaultPrevented, `contenteditable="${spelling}"`).toBe(false)
      editor.remove()
    }
  })

  it('ignores a press that starts inside a popover', () => {
    // On the `contextMenuFor` surfaces the menu is a DOM child of the row. A
    // press on a menu item belongs to the menu: arming here would ramp the tint
    // on every tap, and a 500ms decision hold would swallow the item's click.
    const popover = document.createElement('menu')
    popover.setAttribute('popover', 'auto')
    const item = document.createElement('button')
    popover.appendChild(item)
    row.appendChild(popover)

    item.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    vi.runAllTimers()

    expect(onOpen).not.toHaveBeenCalled()
    expect(row.hasAttribute('data-press-hold')).toBe(false)

    const e = new MouseEvent('contextmenu', { clientX: 95, bubbles: true, cancelable: true })
    item.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(false)
  })

  // The membership pin for `EMBEDDED_UI_SELECTOR`, which is composed:
  // `INPUT_OR_EDITABLE_SELECTOR` supplies the text-entry group and `[popover]`
  // is this gesture's own. The three tests above give each member its rationale
  // one at a time; these two hold the BOUNDARY of the list, which is what an
  // edit to the shared fragment moves.
  describe('embedded-UI boundary', () => {
    /** Press `target`, hold past the threshold, then release. */
    function holdOn(target: Element) {
      onOpen.mockClear()
      target.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      target.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()
    }

    it('declines a press inside every element the list covers', () => {
      for (const { label, host, target } of [...inputOrEditableHosts(), popoverHost()]) {
        row.appendChild(host)

        holdOn(target)

        expect(onOpen, label).not.toHaveBeenCalled()
        expect(row.hasAttribute('data-press-hold'), label).toBe(false)
        host.remove()
      }
    })

    it('still opens the menu on a long press over a row button', () => {
      // `EMBEDDED_UI_SELECTOR` in ~/lib/dragActivators.ts declines `button`;
      // this one must not, so the two lists cannot merge. A long press anywhere
      // on a row opens that row's menu, and a failed chat row renders its
      // recovery actions as visible buttons -- see `attachRowMenu` in
      // ~/components/chat/MessageBubble.tsx. Touch has no hover toolbar, so a
      // decline here would lose the menu over that part of the row.
      const button = document.createElement('button')
      row.appendChild(button)

      holdOn(button)

      expect(onOpen).toHaveBeenCalledOnce()
    })
  })

  it('keeps a fired hold through another pointer press', () => {
    // A second finger, or a mouse click on a hybrid device, must not kill the
    // hold the first finger is still holding for.
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)

    row.dispatchEvent(pointer('pointerdown', { x: 130, y: 110, pointerId: 2, isPrimary: false }))
    row.dispatchEvent(pointer('pointerdown', { x: 130, y: 110, pointerType: 'mouse' }))

    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('lets the release of a fired hold reach document-level listeners', () => {
    // The drag sensor detaches on this release and the chat scroller drops the
    // pointer from its input set, so the event must keep propagating.
    const documentPointerUp = vi.fn()
    document.addEventListener('pointerup', documentPointerUp)

    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    vi.runAllTimers()

    expect(documentPointerUp).toHaveBeenCalledTimes(1)
    document.removeEventListener('pointerup', documentPointerUp)
  })

  it('flags the fired release for tooltip suppression until the menu opens', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
    vi.advanceTimersByTime(HOLD_MS)
    expect(touchReleaseOpensMenu()).toBe(false)

    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
    expect(touchReleaseOpensMenu()).toBe(true)

    vi.runAllTimers()
    expect(touchReleaseOpensMenu()).toBe(false)
  })

  it('ignores a move belonging to a different pointer', () => {
    row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110, pointerId: 1 }))
    row.dispatchEvent(pointer('pointermove', { x: 400, y: 400, pointerId: 2 }))
    vi.advanceTimersByTime(HOLD_MS)
    row.dispatchEvent(pointer('pointerup', { x: 90, y: 110, pointerId: 1 }))
    vi.runAllTimers()

    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  describe('contextmenu', () => {
    /** Windows dispatches `contextmenu` on button RELEASE, so no button is down. */
    function rightClick(x = 150): MouseEvent {
      const e = new MouseEvent('contextmenu', { clientX: x, buttons: 0, bubbles: true, cancelable: true })
      row.dispatchEvent(e)
      return e
    }

    /** macOS and Linux dispatch `contextmenu` on button PRESS, with button 2 still held. */
    function rightPress(x = 150): MouseEvent {
      const e = new MouseEvent('contextmenu', { clientX: x, buttons: 2, bubbles: true, cancelable: true })
      row.dispatchEvent(e)
      return e
    }

    it('opens at the cursor and suppresses the native menu', () => {
      const e = rightClick(150)
      vi.runAllTimers()

      expect(e.defaultPrevented).toBe(true)
      expect(onOpen).toHaveBeenCalledTimes(1)
      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 150 }))
    })

    it('anchors at the cursor on the viewport\'s top edge, where clientY is 0', () => {
      // clientY 0 is a real coordinate, not the keyboard path's "no position".
      const e = new MouseEvent('contextmenu', { clientX: 150, clientY: 0, buttons: 0, bubbles: true, cancelable: true })
      row.dispatchEvent(e)
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 150, clientY: 0 }))
    })

    it('does not open early on another pointer\'s release while the button is held', () => {
      rightPress(150)

      // A touch tap elsewhere: its release still reports the held secondary
      // button in `buttons`, so it must not open the menu.
      document.dispatchEvent(new PointerEvent('pointerup', {
        pointerId: 9,
        pointerType: 'touch',
        button: 0,
        buttons: 2,
        bubbles: true,
      }))
      vi.runAllTimers()
      expect(onOpen).not.toHaveBeenCalled()

      document.dispatchEvent(new PointerEvent('pointerup', {
        pointerId: 5,
        pointerType: 'mouse',
        button: 2,
        buttons: 0,
        bubbles: true,
      }))
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledTimes(1)
      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 150 }))
    })

    // The popover light-dismiss algorithm hides, on `pointerup`, every popover
    // that was not open at `pointerdown`. Opening while the secondary button is
    // still held therefore shows a menu that vanishes the instant the user lets
    // go -- and stays up only for as long as they keep holding.
    it('waits for the button to be released before opening', () => {
      rightPress(150)
      vi.runAllTimers()

      expect(onOpen).not.toHaveBeenCalled()

      document.dispatchEvent(new PointerEvent('pointerup', { pointerId: 5, pointerType: 'mouse', button: 2, bubbles: true }))
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledTimes(1)
      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 150 }))
    })

    it('opens once when the button is released, not once per release', () => {
      rightPress(150)
      document.dispatchEvent(new PointerEvent('pointerup', { pointerId: 5, pointerType: 'mouse', button: 2, bubbles: true }))
      document.dispatchEvent(new PointerEvent('pointerup', { pointerId: 5, pointerType: 'mouse', button: 0, bubbles: true }))
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledTimes(1)
    })

    it('abandons a held press that the browser cancels', () => {
      rightPress(150)
      document.dispatchEvent(new PointerEvent('pointercancel', { pointerId: 5, pointerType: 'mouse', bubbles: true }))
      vi.runAllTimers()

      expect(onOpen).not.toHaveBeenCalled()
    })

    it('falls back to the row edge for the keyboard menu key, which reports no position', () => {
      rightClick(0)
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 40 })) // the row's left edge
    })

    it('does not claim a click, so the next left-click still reaches the row', () => {
      rightClick(150)
      vi.runAllTimers()

      const click = new MouseEvent('click', { bubbles: true, cancelable: true })
      row.dispatchEvent(click)

      expect(click.defaultPrevented).toBe(false)
    })

    // Android Chrome synthesizes a contextmenu at its own long-press threshold, on
    // top of the one this module's timer already fired.
    it('opens once when it arrives after a touch hold fired', () => {
      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      rightClick(300)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledTimes(1)
      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 90 })) // the press x, not the synthetic event's
    })

    // Where the platform fires it BEFORE this module's timer, the platform wins.
    it('fires an in-flight hold early and still opens once', () => {
      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS / 2)
      rightClick(300)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()

      expect(onOpen).toHaveBeenCalledTimes(1)
      expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ clientX: 90 }))
    })
  })

  describe('selectableText', () => {
    let selectable: HTMLElement
    let detachSelectable: () => void
    let onOpenSelectable: ReturnType<typeof vi.fn<(press: ContextMenuPress) => void>>

    beforeEach(() => {
      selectable = document.createElement('div')
      selectable.textContent = 'a message body'
      document.body.appendChild(selectable)
      onOpenSelectable = vi.fn()
      detachSelectable = attachContextMenuGesture(selectable, {
        onOpen: onOpenSelectable,
        selectableText: true,
      })
    })

    afterEach(() => {
      detachSelectable()
      selectable.remove()
      window.getSelection()?.removeAllRanges()
    })

    it('yields to the native menu when the click lands on selected text', () => {
      selectTextWithRect(selectable, { left: 100, right: 300, top: 50, bottom: 70 })

      const e = new MouseEvent('contextmenu', { clientX: 150, clientY: 60, bubbles: true, cancelable: true })
      selectable.dispatchEvent(e)
      vi.runAllTimers()

      expect(e.defaultPrevented).toBe(false)
      expect(onOpenSelectable).not.toHaveBeenCalled()
    })

    it('opens the app menu when the click lands beside the selection', () => {
      selectTextWithRect(selectable, { left: 100, right: 300, top: 50, bottom: 70 })

      const e = new MouseEvent('contextmenu', { clientX: 400, clientY: 60, bubbles: true, cancelable: true })
      selectable.dispatchEvent(e)
      vi.runAllTimers()

      expect(e.defaultPrevented).toBe(true)
      expect(onOpenSelectable).toHaveBeenCalledTimes(1)
    })

    it('opens the app menu when nothing is selected', () => {
      const e = new MouseEvent('contextmenu', { clientX: 150, clientY: 60, bubbles: true, cancelable: true })
      selectable.dispatchEvent(e)
      vi.runAllTimers()

      expect(e.defaultPrevented).toBe(true)
      expect(onOpenSelectable).toHaveBeenCalledTimes(1)
    })
  })

  describe('options', () => {
    it('honours a caller-supplied hold duration', () => {
      detach()
      const onCustom = vi.fn()
      detach = attachContextMenuGesture(row, { onOpen: onCustom, holdMs: 1000 })

      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()
      // The default threshold is not this gesture's threshold.
      expect(onCustom).not.toHaveBeenCalled()

      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(1000)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()
      expect(onCustom).toHaveBeenCalledTimes(1)
    })

    it('honours a caller-supplied slop', () => {
      detach()
      const onCustom = vi.fn()
      detach = attachContextMenuGesture(row, { onOpen: onCustom, slopPx: 40 })

      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      // Past the default slop, inside this gesture's own.
      row.dispatchEvent(pointer('pointermove', { x: 90, y: 110 + PRESS_SLOP_PX + 5 }))
      vi.advanceTimersByTime(HOLD_MS)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      vi.runAllTimers()

      expect(onCustom).toHaveBeenCalledTimes(1)
    })
  })

  describe('detach', () => {
    it('makes every listener inert', () => {
      detach()

      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      row.dispatchEvent(pointer('pointerup', { x: 90, y: 110 }))
      const menu = new MouseEvent('contextmenu', { clientX: 150, bubbles: true, cancelable: true })
      row.dispatchEvent(menu)
      vi.runAllTimers()

      expect(onOpen).not.toHaveBeenCalled()
      expect(menu.defaultPrevented).toBe(false)
      expect(row.hasAttribute('data-press-hold')).toBe(false)

      // afterEach detaches again; that must stay safe.
      detach = () => {}
    })

    it('drops the indicator attribute', () => {
      expect(row.hasAttribute('data-ctx-menu')).toBe(true)
      detach()
      expect(row.hasAttribute('data-ctx-menu')).toBe(false)
      detach = () => {}
    })

    it('cancels a scheduled open', () => {
      // The keyboard / Windows path, which reports no buttons and schedules its
      // open rather than opening from a hold that has already run.
      row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
      detach()
      vi.runAllTimers()

      expect(onOpen).not.toHaveBeenCalled()
      detach = () => {}
    })

    it('ends the inert window when the row detaches mid-hold', () => {
      row.dispatchEvent(pointer('pointerdown', { x: 90, y: 110 }))
      vi.advanceTimersByTime(HOLD_MS)
      expect(heldOverMenu()).toBe(true)

      detach()
      detach = () => {}

      // A row unmounted under a finger -- the chat list churns them on every
      // fling -- must not leave every open menu refusing pointers.
      expect(heldOverMenu()).toBe(false)
    })
  })
})
