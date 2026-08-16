import type { GuardedDragRow } from '~/lib/dragRow'
import { cleanup, render } from '@solidjs/testing-library'
import { DragDropProvider, SortableProvider } from '@thisbeyond/solid-dnd'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { GuardedPointerSensor } from '~/components/shell/guardedPointerSensor'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedDraggableRow, createGuardedSortableRow } from '~/lib/dragRow'
import { flush } from '~/test-support/async'
import { pointerEvent } from '~/test-support/pointer'

/**
 * The factories are tested UNMOCKED against the real library and the real
 * sensor: the protocol they own only means anything as a whole — node-only
 * registration feeding the sensor activators, the guard keeping touch off the
 * body, the grip carrying it, and the transform style appearing only mid-drag.
 */
describe('guarded drag rows', () => {
  afterEach(() => {
    cleanup()
  })

  /**
   * A provider holding one row wired exactly the way an adopting site wires
   * it: `.ref` registration, guarded body activators, a raw grip. `onDragStart`
   * records what activated, plus the row's observable state at that moment.
   */
  function renderProbe(kind: 'sortable' | 'draggable') {
    let activeId: string | null = null
    let observed: { isActiveDraggable: boolean, transform: string | undefined } | undefined
    let row!: GuardedDragRow
    let rowEl!: HTMLDivElement
    let gripEl!: HTMLSpanElement

    function Row() {
      row = kind === 'sortable'
        ? createGuardedSortableRow('row-1')
        : createGuardedDraggableRow('row-1')
      const [body, setBody] = createSignal<HTMLDivElement>()
      const [grip, setGrip] = createSignal<HTMLSpanElement>()
      // eslint-disable-next-line solid/reactivity -- read inside attachDragActivators' effect
      attachDragActivators(body, row.bodyActivators, { touch: 'block' })
      // eslint-disable-next-line solid/reactivity -- read inside attachDragActivators' effect
      attachDragActivators(grip, row.gripActivators, { touch: 'allow' })
      return (
        <div
          ref={(el) => {
            rowEl = el
            setBody(el)
            row.ref(el)
          }}
        >
          <span
            ref={(el) => {
              gripEl = el
              setGrip(el)
            }}
            data-drag-handle=""
          >
            grip
          </span>
        </div>
      )
    }

    render(() => (
      <DragDropProvider
        onDragStart={({ draggable }) => {
          activeId = String(draggable.id)
          observed = { isActiveDraggable: row.isActiveDraggable, transform: row.style()?.transform }
        }}
      >
        <GuardedPointerSensor />
        {/* createSortable requires a sortable-context ancestor, exactly as
            every adopting site provides. */}
        <SortableProvider ids={['row-1']}>
          <Row />
        </SortableProvider>
      </DragDropProvider>
    ))

    return {
      row: () => row,
      rowEl: () => rowEl,
      gripEl: () => gripEl,
      activeId: () => activeId,
      observed: () => observed,
    }
  }

  it('carries no transform style and reports no active drag while at rest', async () => {
    const p = renderProbe('sortable')
    await flush()

    expect(p.row().style()).toEqual({})
    expect(p.row().isActiveDraggable).toBe(false)
    expect(p.activeId()).toBeNull()
  })

  it('a mouse press that travels on the row body starts the drag', async () => {
    const p = renderProbe('sortable')
    await flush()

    p.rowEl().dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'mouse' }))

    expect(p.activeId()).toBe('row-1')
    // The transform arrives with the drag's first tracked move, not at
    // activation itself.
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'mouse' }))
    expect(p.observed()?.isActiveDraggable).toBe(true)
    expect(p.row().isActiveDraggable).toBe(true)
    expect(p.row().style().transform).toMatch(/translate3d/)
  })

  it('a touch press on the row body never starts a drag', async () => {
    const p = renderProbe('sortable')
    await flush()

    p.rowEl().dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 200, pointerType: 'touch' }))

    expect(p.activeId()).toBeNull()
  })

  it('a touch press on the grip starts the drag', async () => {
    const p = renderProbe('sortable')
    await flush()

    p.gripEl().dispatchEvent(pointerEvent('pointerdown', { x: 5, y: 5, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 5, y: 40, pointerType: 'touch' }))

    expect(p.activeId()).toBe('row-1')
  })

  it('the draggable variant keeps the same protocol', async () => {
    const p = renderProbe('draggable')
    await flush()

    p.rowEl().dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'mouse' }))

    expect(p.activeId()).toBe('row-1')
  })
})
