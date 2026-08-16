import { cleanup, render } from '@solidjs/testing-library'
import { createDraggable, DragDropProvider } from '@thisbeyond/solid-dnd'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { pointerEvent } from '~/test-support/pointer'
import { ACTIVATION_DELAY_MS, ACTIVATION_DISTANCE_PX, GuardedPointerSensor } from './guardedPointerSensor'

describe('guardedPointerSensor', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('keeps the two touch-press protocols ordered: menu slop below drag distance, mouse delay below menu hold', () => {
    // The sensor and the context-menu gesture share one press. A finger that
    // starts to drag must abandon the menu hold BEFORE crossing the drag
    // activation distance, and the mouse-only activation delay must come
    // before the menu's hold -- or the row lifts under a press that is still
    // meant to open a menu. The constants live in two modules, so nothing
    // else holds them to this.
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
        <GuardedPointerSensor />
        <Row />
      </DragDropProvider>
    ))

    return { rowEl, activeDraggableId: () => activeId }
  }

  it('does not start a drag from a stationary touch hold, however long', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(motion.longPress * 2)

    // The hold belongs to the context menu; nothing lifts the row on its own.
    expect(activeDraggableId()).toBeNull()
  })

  it('starts a touch drag as soon as the finger travels past the distance', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'touch' }))

    expect(activeDraggableId()).toBe('row-1')
  })

  it('stops treating a touch press as a drag candidate once the menu hold owns it', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(motion.longPress)
    // The menu is open by now; a late move must not start a drag under it.
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 200, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('still starts a drag from a stationary mouse hold', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)

    expect(activeDraggableId()).toBe('row-1')
  })

  it('ignores a non-primary button', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse', button: 2 }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS * 2)

    expect(activeDraggableId()).toBeNull()
  })

  it('ignores a secondary finger entirely', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch', pointerId: 2, isPrimary: false }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'touch', pointerId: 2, isPrimary: false }))

    expect(activeDraggableId()).toBeNull()
  })

  it('ignores a press that starts inside an inline input', () => {
    // The rename inputs on these rows keep their native selection gestures --
    // the same exemption the sibling context-menu gesture applies.
    const { rowEl, activeDraggableId } = renderProbe()
    const input = document.createElement('input')
    rowEl.appendChild(input)

    input.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'mouse' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('ignores a press that starts inside a row menu popover', () => {
    // A row's context menu is a DOM child of the row. A press on a menu item
    // belongs to the menu, and a drag must not start from under it.
    const { rowEl, activeDraggableId } = renderProbe()
    const popover = document.createElement('menu')
    popover.setAttribute('popover', 'auto')
    rowEl.appendChild(popover)

    popover.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'mouse' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('unwinds on pointercancel instead of leaking document listeners', () => {
    const { rowEl } = renderProbe()
    const removeSpy = vi.spyOn(document, 'removeEventListener')

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointercancel', { pointerType: 'touch' }))

    const removed = removeSpy.mock.calls.map(call => call[0])
    expect(removed).toContain('pointermove')
    expect(removed).toContain('pointerup')
    expect(removed).toContain('pointercancel')

    removeSpy.mockRestore()
  })

  it('does not activate after a cancelled touch press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS)
    document.dispatchEvent(pointerEvent('pointercancel', { pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 200, pointerType: 'touch' }))

    expect(activeDraggableId()).toBeNull()
  })

  it('does not activate after a cancelled mouse press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointercancel', { pointerType: 'mouse' }))
    vi.advanceTimersByTime(ACTIVATION_DELAY_MS * 2)

    expect(activeDraggableId()).toBeNull()
  })

  it('clears a superseded press\'s activation timer, so it cannot arm the next press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    // Press A at t=0, superseded by press B at t=100. A's activation timer
    // would fire at t=250; without the clear it would start a drag of A's row
    // while only B is held.
    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    vi.advanceTimersByTime(100)
    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse', pointerId: 3 }))
    vi.advanceTimersByTime(200)
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerId: 3, pointerType: 'mouse' }))

    // B's own hold timer was reset by its attach; the move belongs to B and
    // activates B's row only after B's timer or B's travel -- here the move
    // past the distance activates it.
    expect(activeDraggableId()).toBe('row-1')
  })

  it('a secondary finger\'s release does not end the primary press', () => {
    const { rowEl, activeDraggableId } = renderProbe()

    // The primary finger presses and starts a drag by moving.
    rowEl.dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch', pointerId: 1 }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'touch', pointerId: 1 }))
    expect(activeDraggableId()).toBe('row-1')

    // A second finger taps elsewhere and lifts: its pointerup must not detach
    // the tracked press, or the drag below would die mid-gesture.
    document.dispatchEvent(pointerEvent('pointerup', { x: 300, y: 300, pointerType: 'touch', pointerId: 2 }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'touch', pointerId: 1 }))

    expect(activeDraggableId()).toBe('row-1')
  })
})
