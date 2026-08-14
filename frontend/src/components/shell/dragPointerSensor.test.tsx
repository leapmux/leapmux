import { cleanup, render } from '@solidjs/testing-library'
import { createDraggable, DragDropProvider } from '@thisbeyond/solid-dnd'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { pointerEvent } from '~/test-support/pointer'
import { ACTIVATION_DELAY_MS, ACTIVATION_DISTANCE_PX, DragPointerSensor } from './dragPointerSensor'

/** The sensor's own events are mouse presses; the shared factory defaults to one. */
function pointer(type: string, opts: { x?: number, y?: number, pointerType?: string, pointerId?: number, isPrimary?: boolean, button?: number } = {}): PointerEvent {
  return pointerEvent(type, opts)
}

describe('dragPointerSensor', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('keeps the two touch-press protocols ordered: menu slop below drag distance, drag claim below menu hold', () => {
    // The sensor and the context-menu gesture share one press. A finger that
    // starts to drag must abandon the menu hold BEFORE crossing the drag
    // activation distance, and the drag's claim must come before the menu's
    // hold -- or the row lifts under a press that is still meant to open a menu.
    // The constants live in two modules, so nothing else holds them to this.
    expect(PRESS_SLOP_PX).toBeLessThan(ACTIVATION_DISTANCE_PX)
    expect(ACTIVATION_DELAY_MS).toBeLessThan(motion.longPress)
  })

  /**
   * A `DragDropProvider` holding one draggable row and the sensor under test.
   * `onDragStart` records what the library actually activated, which keeps the
   * assertions off solid-dnd's internal store.
   */
  function renderProbe() {
    let activeId: string | null = null
    let rowEl!: HTMLDivElement

    function Row() {
      const draggable = createDraggable('row-1')
      return (
        <div
          ref={(el) => {
            rowEl = el
            draggable(el)
          }}
        >
          row
        </div>
      )
    }

    render(() => (
      <DragDropProvider onDragStart={({ draggable }) => { activeId = String(draggable.id) }}>
        <DragPointerSensor />
        <Row />
      </DragDropProvider>
    ))

    return { rowEl, activeDraggableId: () => activeId }
  }

  it('does not start a drag from a stationary touch hold', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(motion.longPress * 2)

    // The hold belongs to the context menu; the claim never lifts the row on its own.
    expect(activeDraggableId()).toBeNull()
  })

  it('starts a touch drag when the finger moves after the claim', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 70, pointerType: 'touch' }))

    expect(activeDraggableId()).toBe('row-1')
  })

  it('treats a touch that moves before the claim as a scroll, however far it travels', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 400, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('blocks the browser pan while the claim stands, so the drag wins the race', () => {
    const { rowEl } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))

    // Before the claim: the move belongs to the scroller, untouched.
    const early = pointer('pointermove', { x: 50, y: 55, pointerType: 'touch' })
    document.dispatchEvent(early)
    expect(early.defaultPrevented).toBe(false)

    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)

    const claimedMove = pointer('pointermove', { x: 50, y: 53, pointerType: 'touch' })
    document.dispatchEvent(claimedMove)
    expect(claimedMove.defaultPrevented).toBe(true)
  })

  it('hands the press back at the context menu threshold', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(motion.longPress)

    // The claim is gone, so a late drag cannot start on top of the open menu.
    const late = pointer('pointermove', { x: 50, y: 200, pointerType: 'touch' })
    document.dispatchEvent(late)

    expect(late.defaultPrevented).toBe(false)
    expect(activeDraggableId()).toBeNull()
  })

  // The test that stops the fork drifting into "we broke desktop drag".
  it('still starts a drag from a stationary mouse hold', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)

    expect(activeDraggableId()).toBe('row-1')
  })

  it('ignores a non-primary button', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(new PointerEvent('pointerdown', {
      clientX: 50,
      clientY: 50,
      pointerId: 1,
      pointerType: 'mouse',
      button: 2,
      isPrimary: true,
      bubbles: true,
      cancelable: true,
    }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS * 2)

    expect(activeDraggableId()).toBeNull()
  })

  it('unwinds on pointercancel instead of leaking document listeners', () => {
    const { rowEl } = renderProbe()
    const removeSpy = vi.spyOn(document, 'removeEventListener')

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointer('pointercancel', { pointerType: 'touch' }))

    const removed = removeSpy.mock.calls.map(call => call[0])
    expect(removed).toContain('pointermove')
    expect(removed).toContain('pointerup')
    expect(removed).toContain('pointercancel')

    removeSpy.mockRestore()
  })

  it('does not activate after a cancelled touch press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointer('pointercancel', { pointerType: 'touch' }))
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 200, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('starts a fresh press without the previous one\'s claim', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    // Claim, then let the press end.
    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointer('pointerup', { x: 50, y: 50, pointerType: 'touch' }))

    // A new press that moves at once is a scroll again, not a drag.
    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 400, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('does not activate after a cancelled mouse press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointer('pointercancel', { pointerType: 'mouse' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS * 2)

    expect(activeDraggableId()).toBeNull()
  })

  it('ignores a touch press that starts inside an inline input', () => {
    // The rename inputs on these rows keep their native selection gestures --
    // the same exemption the sibling context-menu gesture applies.
    const { rowEl, activeDraggableId } = renderProbe()
    const input = document.createElement('input')
    rowEl.appendChild(input)

    input.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 90, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('ignores a touch press that starts inside a row menu popover', () => {
    // A row's context menu is a DOM child of the row. A press on a menu item
    // belongs to the menu, and a drag must not start from under it.
    const { rowEl, activeDraggableId } = renderProbe()
    const popover = document.createElement('menu')
    popover.setAttribute('popover', 'auto')
    rowEl.appendChild(popover)

    popover.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 90, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('clears a superseded press\'s claim timer, so it cannot arm the next press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    // Press A at t=0, superseded by press B at t=100. A's claim timer would
    // fire at t=250; without the clear it would mark B's press claimed, and a
    // move at t=300 -- 200ms into B, before B's own claim -- would drag.
    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(100)
    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch', pointerId: 3 }))
    vi.advanceTimersByTime(200)
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 90, pointerId: 3, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('a secondary finger\'s release does not end the primary press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    // The primary finger presses and earns the claim.
    rowEl.dispatchEvent(pointer('pointerdown', { x: 50, y: 50, pointerType: 'touch', pointerId: 1 }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)

    // A second finger taps elsewhere and lifts: its pointerup must not detach
    // the tracked press, or the drag below would die mid-gesture.
    document.dispatchEvent(pointer('pointerup', { x: 300, y: 300, pointerType: 'touch', pointerId: 2 }))
    document.dispatchEvent(pointer('pointermove', { x: 50, y: 70, pointerType: 'touch', pointerId: 1 }))

    expect(activeDraggableId()).toBe('row-1')
  })
})
