import type { Locator, Page } from '@playwright/test'
import { devices, expect } from '@playwright/test'

/**
 * Real touch input for the E2E specs, plus the device metrics that give Blink a coarse pointer.
 *
 * Playwright's own touch API is `page.touchscreen.tap` and nothing else, so a DRAG needs the raw
 * CDP command underneath it. A synthesized `PointerEvent` is not an alternative: the browser
 * rejects `setPointerCapture` for a pointer id that no real input created, so every drag
 * controller in the app bails on the first press and the test proves nothing.
 */

/**
 * Pixel 7's device metrics WITHOUT its `defaultBrowserType`, and with a shorter viewport.
 * Playwright refuses a `defaultBrowserType` inside a describe group -- it would force a new
 * worker -- and this suite's only project is already chromium, so the field is both unusable
 * and redundant. The rest is what actually gives Blink a COARSE primary pointer: it derives the
 * primary pointer type from the mobile viewport, not from `hasTouch`, so metrics that set only
 * `hasTouch` would look like coverage and be none. The height is cut from the device's 915px to
 * 380px: the device is narrower than 720px, so its lines wrap more and one seeded message still
 * overflows comfortably.
 */
export const COARSE_POINTER_METRICS = {
  viewport: { width: devices['Pixel 7'].viewport.width, height: 380 },
  deviceScaleFactor: devices['Pixel 7'].deviceScaleFactor,
  isMobile: devices['Pixel 7'].isMobile,
  hasTouch: devices['Pixel 7'].hasTouch,
} as const

/** A finger held on the screen. Move it, then end it -- both in viewport CSS pixels. */
export interface TouchPointer {
  /** Drag the finger to an absolute viewport position. */
  moveTo: (x: number, y: number) => Promise<void>
  /** Lift the finger and release the CDP session. */
  end: () => Promise<void>
}

/**
 * Press a finger at a viewport position and keep it down, so a test can assert what the page
 * does WHILE the gesture is live (a rail thumb tracking the finger, a preview popover opening)
 * rather than only after it. The context must have `hasTouch` -- see
 * {@link COARSE_POINTER_METRICS} -- or Blink discards the events.
 */
export async function touchDown(page: Page, x: number, y: number): Promise<TouchPointer> {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
  return {
    async moveTo(nextX, nextY) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: nextX, y: nextY }] })
    },
    async end() {
      // touchEnd carries NO touch points: the one that lifted is the one that is gone.
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
      await cdp.detach()
    },
  }
}

/** A viewport position in CSS pixels. */
export interface TouchPoint {
  x: number
  y: number
}

/**
 * Tap a viewport position `taps` times in a row, on one CDP session.
 *
 * The session is opened once and reused, because a multi-tap gesture measures the gap between
 * its taps against a real clock (see `MULTI_TAP_MS` in ~/src/lib/tapSelect.ts) and a session
 * per tap would spend that budget on protocol round trips rather than on the gesture.
 */
export async function touchTap(page: Page, point: TouchPoint, opts: { taps?: number } = {}): Promise<void> {
  const taps = opts.taps ?? 1
  const cdp = await page.context().newCDPSession(page)
  // `finally`, for the reason {@link touchSwipe} states: a protocol error mid-sequence would
  // otherwise leave Blink believing a finger is still down.
  try {
    for (let tap = 0; tap < taps; tap++) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: point.x, y: point.y }] })
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    }
  }
  finally {
    await cdp.detach()
  }
}

/**
 * Hold a still finger at a viewport position for `holdMs`, then lift it.
 *
 * The finger does not move, so this is a LONG PRESS and not a drag: it is what drives the
 * context-menu hold (see `motion.longPress` in ~/src/styles/tokens.ts for the threshold it has
 * to pass). Use {@link touchDown} directly when the test must assert something while the finger
 * is still down.
 */
export async function touchHold(page: Page, point: TouchPoint, holdMs: number): Promise<void> {
  const finger = await touchDown(page, point.x, point.y)
  try {
    await page.waitForTimeout(holdMs)
  }
  finally {
    await finger.end()
  }
}

/**
 * A complete finger swipe along a straight line, in `steps` moves. Use this to drive the page's
 * own touch scrolling and the app's swipe gestures; use {@link touchDown} when the test must
 * assert something mid-gesture.
 *
 * The intermediate moves are the point, on either axis. A recognizer decides its axis from the
 * first travel past its threshold, and the browser decides whether to pan from the same samples,
 * so a single jump from `from` to `to` exercises neither.
 */
export async function touchSwipe(
  page: Page,
  opts: { from: TouchPoint, to: TouchPoint, steps?: number },
): Promise<void> {
  const steps = opts.steps ?? 5
  const finger = await touchDown(page, opts.from.x, opts.from.y)
  // `finally`, so a failed move still lifts the finger and detaches the session. Without it a
  // mid-swipe protocol error would leave Blink believing a finger is still down, and every later
  // touch in the same test would arrive as a second contact point.
  try {
    for (let step = 1; step <= steps; step++) {
      await finger.moveTo(
        opts.from.x + ((opts.to.x - opts.from.x) * step) / steps,
        opts.from.y + ((opts.to.y - opts.from.y) * step) / steps,
      )
    }
  }
  finally {
    await finger.end()
  }
}

/**
 * Wait until the input events already dispatched are RENDERED.
 *
 * CDP acknowledges the dispatch of a pointer event, not its processing on the
 * main thread, so a lift issued straight after the last move can race the
 * dragOver that decides where the drop lands. Two frames is the guarantee: the
 * first callback runs after the main thread consumes the pending work, the
 * second after that work paints.
 *
 * This is what a drag settles on instead of a sleep. A wall-clock wait elapses
 * on schedule however far behind the main thread runs, which makes it exactly
 * wrong under load -- the condition it exists to cover.
 */
export async function settleFrames(page: Page): Promise<void> {
  await page.evaluate(() => new Promise<void>(resolve =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  ))
}

/**
 * Touch-press `grip`, travel past the 10px activation distance, and drag until
 * the DRAGGED ROW's center sits on `target` -- then lift.
 *
 * The finger does not stop at `target` itself: solid-dnd's collision reference
 * is the dragged element's transformed CENTER, which keeps the grip-to-center
 * offset it had at the press. A grip press therefore carries the reference half
 * a row past the finger, and a drop aimed at the finger's position resolves a
 * DIFFERENT droppable (observed: the tab-bar zone, a same-tile no-op) whenever
 * the drag runs right-to-left. Aiming the row's center at the target makes the
 * drop land on the target from any direction.
 *
 * `draggedRow` is also the oracle: the drag's start AND end are confirmed
 * against `draggingClass`, so a press that somehow never activated -- or a lift
 * the drag pipeline never saw -- fails HERE instead of as a mysterious
 * unchanged order later. Each surface specifies that class itself, because the
 * class is the surface's own (`tabDragging`, `itemDragging`).
 *
 * Shared by every grip-drag spec. The gesture's shape is not obvious -- the
 * reference offset above is the part a second copy would get wrong -- so it has
 * ONE home.
 */
export async function touchDragGripOnto(opts: {
  page: Page
  grip: TouchPoint
  target: TouchPoint
  draggedRow: Locator
  draggingClass: RegExp
}): Promise<void> {
  const { page, grip, target, draggedRow, draggingClass } = opts
  const rowBox = (await draggedRow.boundingBox())!
  // The collision reference sits this far right of the finger for the whole
  // gesture (grip press): aim the finger so the reference lands on target.
  const referenceOffsetX = rowBox.x + rowBox.width / 2 - grip.x
  const fingerTarget = { x: target.x - referenceOffsetX, y: target.y }

  const finger = await touchDown(page, grip.x, grip.y)
  try {
    // A move comfortably past the sensor's 10px activation distance, then a
    // short pause for the drag to start before the move to the target --
    // solid-dnd recomputes droppable collisions on every move, the same shape
    // the mouse-driven reorder specs use.
    await finger.moveTo(grip.x + 8, grip.y + 20)
    await expect(draggedRow).toHaveClass(draggingClass)
    const steps = 12
    for (let step = 1; step <= steps; step++) {
      await finger.moveTo(
        grip.x + 8 + ((fingerTarget.x - grip.x - 8) * step) / steps,
        grip.y + 20 + ((fingerTarget.y - grip.y - 20) * step) / steps,
      )
    }
    // Settle before lifting, or a lift that races the final dragOver resolves
    // the drop prematurely.
    await settleFrames(page)
  }
  finally {
    await finger.end()
  }
  // The lift ended the drag: a press whose pointerup the pipeline lost would
  // leave the row lifted and the reorder would never be attempted.
  await expect(draggedRow).not.toHaveClass(draggingClass)
}
