import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { settleFrames } from './touch'
import { boxOf } from './ui'

/**
 * Pointer drags for the E2E specs. The touch drag lives in `./touch.ts` as `touchDragGripOnto`.
 *
 * The app's drag sensor (`~/components/shell/guardedPointerSensor.ts`) starts a mouse drag after 10px of travel or
 * after a 250ms hold, whichever comes first. A drag here starts by travel, so no step waits for a timer, and each
 * drag ends on a rendered frame, never on a sleep: a wall-clock wait elapses on schedule however far behind the
 * main thread runs, so it fails under exactly the load that it guesses at.
 */

/** A viewport position in CSS pixels. */
export interface ViewportPoint {
  x: number
  y: number
}

/** The center of the element's box. It waits for a box, because an element has none before its layout. */
export async function boxCenter(locator: Locator): Promise<ViewportPoint> {
  const box = await boxOf(locator)
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

/**
 * The first move of a drag, relative to the press: past the sensor's 10px activation distance, on both axes, so it
 * does not depend on the side of the press that the target lies on.
 */
const ACTIVATION_MOVE = { x: 8, y: 20 } as const

/** The row that a drag lifts, and the class that the row carries while it is lifted, such as `/tabDragging/`. */
export interface DraggedRow {
  row: Locator
  draggingClass: RegExp
}

/**
 * Drag with the real mouse from `from` onto `to`, and release.
 *
 * - The press is followed by a short move past the activation distance, which starts the drag at once.
 * - The move to the target goes out in `steps` input events, because the drag library recomputes the drop target
 *   on each move. A jump in one event can skip the target.
 * - The release waits for two rendered frames (`settleFrames`), because a release that races the last move
 *   resolves the drop at an earlier position.
 *
 * With `dragged`, the row must carry its dragging class after the activation move and lose it after the release,
 * so a press that never started a drag, or a release that the drag never saw, fails here and not later as an
 * unchanged layout. Omit it for a drag that moves the row out of the page, such as a move to another workspace:
 * the class check after the release needs the row on the page.
 */
export async function mouseDragOnto(page: Page, opts: {
  from: ViewportPoint
  to: ViewportPoint
  steps?: number
  dragged?: DraggedRow
}): Promise<void> {
  const steps = opts.steps ?? 12
  if (!Number.isSafeInteger(steps) || steps < 1)
    throw new RangeError(`A drag needs a positive whole number of steps, not ${steps}.`)
  const { from, to, dragged } = opts
  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  // `finally`, so a failed check still releases the button. A held button would turn every later mouse action of
  // the test into a drag.
  try {
    await page.mouse.move(from.x + ACTIVATION_MOVE.x, from.y + ACTIVATION_MOVE.y)
    if (dragged)
      await expect(dragged.row, 'the press started a drag').toHaveClass(dragged.draggingClass)
    await page.mouse.move(to.x, to.y, { steps })
    await settleFrames(page)
  }
  finally {
    await page.mouse.up()
  }
  if (dragged)
    await expect(dragged.row, 'the release ended the drag').not.toHaveClass(dragged.draggingClass)
}

/**
 * Drag a sidebar tab leaf onto `target` with synthetic pointer events, dispatched in the page.
 *
 * The real mouse cannot press a leaf: the workspace row above it is a sortable item with a `translate3d` transform,
 * which makes a stacking context that covers the leaf, so the press would hit the row. The sensor listens for its
 * moves and its release on `document`, and it starts the drag by travel, so synthetic events drive it the same way.
 * The press is 10px inside the leaf's left edge, where the label starts.
 */
export async function dragSidebarLeafTo(leaf: Locator, target: ViewportPoint): Promise<void> {
  await leaf.evaluate(async (element, { targetX, targetY, activation }) => {
    const pointer = (type: string, clientX: number, clientY: number, buttons: number) => new PointerEvent(type, {
      clientX,
      clientY,
      pointerId: 1,
      button: 0,
      buttons,
      isPrimary: true,
      bubbles: true,
      cancelable: true,
    })
    const frames = () => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    const rect = element.getBoundingClientRect()
    const startX = rect.x + 10
    const startY = rect.y + rect.height / 2
    element.dispatchEvent(pointer('pointerdown', startX, startY, 1))
    // Past the activation distance first, as `mouseDragOnto` does, so the drag starts before the moves to the target.
    document.dispatchEvent(pointer('pointermove', startX + activation.x, startY + activation.y, 1))
    const steps = 10
    for (let step = 1; step <= steps; step++) {
      const x = startX + activation.x + ((targetX - startX - activation.x) * step) / steps
      const y = startY + activation.y + ((targetY - startY - activation.y) * step) / steps
      document.dispatchEvent(pointer('pointermove', x, y, 1))
    }
    await frames()
    document.dispatchEvent(pointer('pointerup', targetX, targetY, 0))
    await frames()
  }, { targetX: target.x, targetY: target.y, activation: ACTIVATION_MOVE })
}
