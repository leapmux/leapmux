import type { Page } from '@playwright/test'
import { devices } from '@playwright/test'

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
