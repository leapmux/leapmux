import type { DragActivatorProps } from '~/lib/dragActivators'
import { createDraggable, createSortable, maybeTransformStyle } from '@thisbeyond/solid-dnd'

/**
 * The subset of solid-dnd's sortable/draggable that the grip-drag protocol
 * consumes. Structural on purpose: the factories never let the primitive
 * itself escape.
 */
interface DragPrimitive {
  ref: (el: HTMLElement) => void
  readonly dragActivators: DragActivatorProps
  readonly isActiveDraggable: boolean
  readonly transform: { x: number, y: number }
}

/**
 * A draggable row wired for the grip-drag protocol: node-only registration,
 * activation split between a guarded row body and a raw grip, and the drag
 * transform as a ready-made style.
 *
 * This exists so the protocol has ONE owner. The library's own call form
 * (`sortable(el)` / `draggable(el)`) attaches the sensor activators
 * wholesale — touch included — which is the exact defect the split exists to
 * prevent, and every hand-wired copy of the protocol was one habit away from
 * reintroducing it. The primitive never escapes this interface, so that call
 * form is not available at an adopting site.
 *
 * The wiring contract at the site:
 *
 * - `ref` on the row element (never the primitive's call form).
 * - `attachDragActivators(rowEl, row.bodyActivators, { touch: 'block' })` on
 *   the row body's anchor.
 * - `<DragHandle activators={row.gripActivators} />` for the grip.
 * - `row.style()` in the row's style prop (`undefined` while at rest, so an
 *   idle row carries no transform).
 */
export interface GuardedDragRow {
  ref: (el: HTMLElement) => void
  /** The row body's activators — raw touches and embedded-UI presses stay out. */
  bodyActivators: () => DragActivatorProps | undefined
  /** The grip's activators — raw, touch included. */
  gripActivators: () => DragActivatorProps | undefined
  /**
   * The drag transform as a style object; empty while at rest, so an idle
   * row carries no transform (and gains no stacking context).
   */
  style: () => { transform?: string }
  /** Whether this row is the drag currently in flight. */
  readonly isActiveDraggable: boolean
}

/**
 * The axis a list reorders along, which is the axis its rows may move.
 *
 * The library transforms a dragged row on BOTH axes, so a row in a vertical
 * list follows the pointer sideways as well. It then reaches past the
 * container's content box, and a container that scrolls vertically computes
 * `overflow-x` to `auto` and raises a horizontal scrollbar. Hiding that
 * overflow per list treats the symptom and clips whatever else overflows the
 * row; pinning the off-axis to zero removes the sideways travel at its source.
 *
 * `both` stays the default, for a surface that really does move on two axes.
 */
export type DragAxis = 'x' | 'y' | 'both'

/**
 * The transform with the off-axis pinned to zero.
 *
 * Passes the value through UNTOUCHED for `both`, and for anything that is not
 * the pair the library documents. `maybeTransformStyle` already tolerates a
 * primitive that carries no transform, so reading `.x` off one here would be
 * the one place that does not.
 */
function axisTransform<T>(transform: T, axis: DragAxis): T | { x: number, y: number } {
  const point = transform as { x?: number, y?: number } | undefined
  if (axis === 'both' || typeof point?.x !== 'number' || typeof point?.y !== 'number')
    return transform
  return axis === 'y' ? { x: 0, y: point.y } : { x: point.x, y: 0 }
}

function guardRow(primitive: DragPrimitive | undefined, axis: DragAxis): GuardedDragRow {
  return {
    ref: el => primitive?.ref?.(el),
    bodyActivators: () => primitive?.dragActivators,
    gripActivators: () => primitive?.dragActivators,
    style: () => (primitive ? maybeTransformStyle(axisTransform(primitive.transform, axis) as { x: number, y: number }) : {}),
    get isActiveDraggable() {
      return primitive?.isActiveDraggable ?? false
    },
  }
}

/**
 * A sortable row (`createSortable` under the guard). Returns a no-op row when
 * the drag context is not available — the ErrorBoundary fallbacks rely on the
 * rest of the row (menu anchor, rendering) surviving that.
 */
export function createGuardedSortableRow(
  key: string,
  data?: Record<string, unknown>,
  axis: DragAxis = 'both',
): GuardedDragRow {
  let sortable: DragPrimitive | undefined
  try {
    sortable = data !== undefined ? createSortable(key, data) : createSortable(key)
  }
  catch { /* DnD context not ready */ }
  return guardRow(sortable, axis)
}

/** A draggable (non-sortable) row (`createDraggable` under the same guard). */
export function createGuardedDraggableRow(
  key: string,
  data?: Record<string, unknown>,
  axis: DragAxis = 'both',
): GuardedDragRow {
  let draggable: DragPrimitive | undefined
  try {
    draggable = data !== undefined ? createDraggable(key, data) : createDraggable(key)
  }
  catch { /* DnD context not ready */ }
  return guardRow(draggable, axis)
}
