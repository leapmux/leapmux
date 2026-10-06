import type { Page } from '@playwright/test'

/**
 * Waits that end at a rendered frame of the page, for the E2E specs and helpers.
 *
 * A wall-clock wait elapses on schedule however far behind the main thread runs. That makes it wrong under load,
 * which is the condition that it exists to cover. A wait here ends at a step of the page's own rendering instead.
 */

/**
 * Wait until the work that the page already queued is RENDERED: dispatched input events, the state changes that they
 * caused, and the `requestAnimationFrame` callbacks that those changes scheduled.
 *
 * CDP acknowledges the dispatch of an input event, not its processing on the main thread. So a mouse release or a
 * finger lift that a test issues straight after the last move can race the `dragOver` that decides where the drop
 * lands. Two frames are the guarantee:
 *
 * - The first callback runs after the main thread consumed the pending work.
 * - The second callback runs after that work painted.
 *
 * A callback that the page schedules during the first frame runs in the second frame, in the same rendering step as
 * the second callback. That step runs each callback of the frame before the page takes a new task, so the next
 * command of the test arrives after it.
 */
export async function settleFrames(page: Pick<Page, 'evaluate'>): Promise<void> {
  await page.evaluate(() => new Promise<void>(resolve =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  ))
}
