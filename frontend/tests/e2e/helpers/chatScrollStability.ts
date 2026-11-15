import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { assistantBubbles, CHAT_SCROLL_CONTAINER, chatDistanceFromBottom, chatScrollContainer, sendMessage, waitForAgentIdle } from './ui'

/**
 * Reads and actions for the specs that check that the chat view keeps what the reader sees in place
 * (`048*-chat-scroll-stability*.spec.ts`).
 *
 * Each read of a row runs in ONE `evaluate` on the scroll container, which the app never rebuilds. A read through a
 * chat locator could hold a row that ChatView replaced between the resolve and the read (see `readAttached`). The
 * functions that run in the browser (`topRowIn`, `rowDistanceIn`, `topParagraphIn`) reference nothing outside their
 * own body, because Playwright sends only their source text to the page.
 */

/** A row of the chat, and the distance from the top edge of the scroll container to the top edge of the row. */
export interface RowDistance {
  seq: string
  distance: number
}

/** A paragraph that a marker such as `P17:` identifies, near the top of the chat. */
export interface TopParagraph {
  marker: string
  /** The distance from the top edge of the scroll container to the top edge of the paragraph. */
  distance: number
  /** The height of the row that holds the paragraph. */
  rowHeight: number
}

/**
 * The row at the top of `container`: of the rows whose bottom edge is below the top edge of the container, the one
 * whose top edge is highest. The distance is negative when the row starts above the container. Runs in the browser.
 */
export function topRowIn(container: Element): RowDistance | null {
  const top = container.getBoundingClientRect().top
  let found: RowDistance | null = null
  for (const row of Array.from(container.querySelectorAll<HTMLElement>('[data-seq]'))) {
    const rect = row.getBoundingClientRect()
    const seq = row.dataset.seq
    if (seq === undefined || rect.bottom <= top || (found !== null && rect.top - top >= found.distance))
      continue
    found = { seq, distance: rect.top - top }
  }
  return found
}

/**
 * The distance from the top edge of `container` to the top edge of the row of `seq`, or null when the row is not
 * mounted. Runs in the browser.
 */
export function rowDistanceIn(container: Element, seq: string): number | null {
  const row = container.querySelector(`[data-seq="${seq}"]`)
  return row === null
    ? null
    : row.getBoundingClientRect().top - container.getBoundingClientRect().top
}

/**
 * The first paragraph, in document order, whose text starts with a match of `markerSource` and whose top edge is at
 * or below the top edge of `container`. Null when no such paragraph exists or it has no row. Runs in the browser.
 */
export function topParagraphIn(container: Element, markerSource: string): TopParagraph | null {
  const marker = new RegExp(markerSource)
  const top = container.getBoundingClientRect().top
  for (const paragraph of Array.from(container.querySelectorAll('p'))) {
    const match = marker.exec(paragraph.textContent ?? '')
    const distance = paragraph.getBoundingClientRect().top - top
    if (match === null || distance < 0)
      continue
    const row = paragraph.closest('[data-seq]')
    if (row === null)
      return null
    return { marker: match[0], distance, rowHeight: row.getBoundingClientRect().height }
  }
  return null
}

/** Read the row at the top of the chat (see topRowIn). */
export function readTopRow(page: Page): Promise<RowDistance | null> {
  return chatScrollContainer(page).evaluate(topRowIn)
}

/** Read the distance of the row of `seq` from the top of the chat (see rowDistanceIn). */
export function readRowDistance(page: Page, seq: string): Promise<number | null> {
  return chatScrollContainer(page).evaluate(rowDistanceIn, seq)
}

/** Read the paragraph near the top of the chat whose text starts with a match of `marker` (see topParagraphIn). */
export function readTopParagraph(page: Page, marker: RegExp): Promise<TopParagraph | null> {
  return chatScrollContainer(page).evaluate(topParagraphIn, marker.source)
}

/** Read the row at the top of the chat, and fail when no row is there. */
export async function requireTopRow(page: Page): Promise<RowDistance> {
  const row = await readTopRow(page)
  expect(row, 'a row is at the top of the view').not.toBeNull()
  return row!
}

/** Wait until the chat stops scrolling: two reads of `scrollTop`, one frame apart, are equal. */
export async function waitForScrollRest(page: Page): Promise<void> {
  await expect.poll(() => chatScrollContainer(page).evaluate(container => new Promise<boolean>((resolve) => {
    const before = container.scrollTop
    requestAnimationFrame(() => resolve(container.scrollTop === before))
  })), { message: 'the chat stops scrolling' }).toBe(true)
}

/**
 * Wait until the thinking indicator of a running turn ends its entry animation, then until the chat rests. The
 * indicator grows over a CSS transition, and it fades in only after that transition (see ThinkingIndicator), so a full
 * opacity marks its final height. A follower re-sticks to the bottom while the indicator grows, so a row that a test
 * reads during the growth moves for that reason, and a check of one frame of rest can pass in the middle of it.
 */
export async function waitForThinkingIndicator(page: Page): Promise<void> {
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCSS('opacity', '1')
  await waitForScrollRest(page)
}

/** Put the pointer over the middle of the chat, where a wheel scrolls it. */
export async function pointAtChat(page: Page): Promise<void> {
  const box = await chatScrollContainer(page).boundingBox()
  expect(box, 'the chat has a box on screen').not.toBeNull()
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
}

/**
 * Wheel the chat up by `pixels`, then wait until the content moves down the screen and the chat stops scrolling.
 * `page.mouse.wheel` only sends the input event and does not wait for the scroll. A rest check right after it can see
 * the view before the scroll starts, so this helper first waits until the row at the top moves down, or leaves the
 * mounted rows. It reads the screen, not `scrollTop`: a rebase at rest after an older page arrives raises `scrollTop`
 * with no move on screen, and new content below a view at the bottom raises it too.
 */
export async function wheelChatUp(page: Page, pixels: number): Promise<void> {
  await pointAtChat(page)
  const before = await requireTopRow(page)
  await page.mouse.wheel(0, -pixels)
  await expect.poll(async () => await readRowDistance(page, before.seq) ?? Number.POSITIVE_INFINITY, {
    message: 'the wheel moves the content down the screen',
  }).toBeGreaterThan(before.distance)
  await waitForScrollRest(page)
}

/** Expect that the row of `before` is still mounted, at most 1px from its distance in `before`. */
export async function expectRowHeld(page: Page, before: RowDistance, when: string): Promise<void> {
  await expectRowMoved(page, before, 0, when)
}

/**
 * Expect that the row of `before` is still mounted, and that it moved down the screen by `pixels` from its distance in
 * `before`, within 1px. A wheel of `pixels` up moves every row down by exactly that much, so any other move is a jump.
 */
export async function expectRowMoved(page: Page, before: RowDistance, pixels: number, when: string): Promise<void> {
  const after = await readRowDistance(page, before.seq)
  expect(after, `${when}: the row of seq ${before.seq} stays mounted`).not.toBeNull()
  expect(Math.abs(after! - before.distance - pixels), `${when}: the row of seq ${before.seq} moves by ${pixels}px, within 1px`).toBeLessThanOrEqual(1)
}

/**
 * Read the height of the content of the one chat of the page. It reads a chat that another tab hides too: the shell
 * hides an inactive tab with `visibility: hidden`, so the chat keeps its layout. `chatScrollContainer` matches a
 * visible chat only, so this read uses the selector with no visibility filter.
 */
export function readScrollHeight(page: Page): Promise<number> {
  return page.locator(CHAT_SCROLL_CONTAINER).evaluate(container => container.scrollHeight)
}

/** Read the width of the one chat of the page, visible or hidden (see readScrollHeight). */
export function readChatWidth(page: Page): Promise<number> {
  return page.locator(CHAT_SCROLL_CONTAINER).evaluate(container => container.clientWidth)
}

/** Read the height of the viewport of the chat. */
export function readViewportHeight(page: Page): Promise<number> {
  return chatScrollContainer(page).evaluate(container => container.clientHeight)
}

/** Expect that the view rests at the bottom of the content, within the 2px tolerance of follow mode. */
export async function expectAtBottom(page: Page, when: string): Promise<void> {
  await waitForScrollRest(page)
  await expect.poll(() => chatDistanceFromBottom(page), { message: `${when}: the view is at the bottom` }).toBeLessThanOrEqual(2)
}

/** What a watch of one row saw over the frames that it ran. */
export interface RowWatchResult {
  /** The animation frames that the watch sampled. */
  frames: number
  /** The frames in which the row was not mounted. */
  missingFrames: number
  /** The smallest and the largest distance from the top edge of the container to the top edge of the row. */
  min: number
  max: number
}

/** The watch that `watchRow` starts. */
export interface RowWatch {
  /** Stop the watch, and expect that the row stayed mounted and moved by at most 1px in every frame. */
  expectStill: (when: string) => Promise<RowWatchResult>
}

/** The property of `window` that holds the state of the running watch. One watch runs at a time. */
const ROW_WATCH_KEY = '__chatScrollStabilityRowWatch'

/** The state of a running watch, which the page keeps on `window` under the key of the watch. */
interface RowWatchState extends RowWatchResult {
  stopped: boolean
}

/**
 * Sample the distance of the row of `rowSeq` from the top edge of `container` in every animation frame, and keep the
 * state on `window` under `key`. Runs in the browser.
 */
export function startRowWatchIn(container: Element, { rowSeq, key }: { rowSeq: string, key: string }): void {
  const state: RowWatchState = {
    frames: 0,
    missingFrames: 0,
    min: Number.POSITIVE_INFINITY,
    max: Number.NEGATIVE_INFINITY,
    stopped: false,
  }
  const sample = () => {
    if (state.stopped)
      return
    const row = container.querySelector(`[data-seq="${rowSeq}"]`)
    if (row === null) {
      state.missingFrames++
    }
    else {
      const distance = row.getBoundingClientRect().top - container.getBoundingClientRect().top
      state.min = Math.min(state.min, distance)
      state.max = Math.max(state.max, distance)
    }
    state.frames++
    requestAnimationFrame(sample)
  }
  Reflect.set(window, key, state)
  requestAnimationFrame(sample)
}

/** Stop the watch under `key`, and return what it saw, or null when no watch runs under it. Runs in the browser. */
export function stopRowWatchIn(key: string): RowWatchResult | null {
  const state = Reflect.get(window, key) as RowWatchState | undefined
  if (state === undefined)
    return null
  state.stopped = true
  Reflect.deleteProperty(window, key)
  return {
    frames: state.frames,
    missingFrames: state.missingFrames,
    min: state.min,
    max: state.max,
  }
}

/**
 * Sample the distance of the row of `seq` from the top of the chat in every animation frame, until the caller stops
 * the watch. A check at rest sees only the end state. The watch also sees a move that a later frame corrects, which
 * the reader sees as a jump all the same.
 *
 * A frame callback runs before the layout of its frame, so a sample shows the layout of the last frame. The rows of the
 * chat move only by their offsets and the layout shift, which the app writes in one synchronous update, so no sample
 * catches a state between the two. A move inside a row, such as a rewrap, is different: the shift corrects it one frame
 * later, so do not watch a row while the width changes.
 */
export async function watchRow(page: Page, seq: string): Promise<RowWatch> {
  await chatScrollContainer(page).evaluate(startRowWatchIn, { rowSeq: seq, key: ROW_WATCH_KEY })
  return {
    expectStill: async (when) => {
      const result = await page.evaluate(stopRowWatchIn, ROW_WATCH_KEY)
      expect(result, `${when}: the watch of seq ${seq} ran`).not.toBeNull()
      expect(result!.frames, `${when}: the watch of seq ${seq} sampled frames`).toBeGreaterThan(1)
      expect(result!.missingFrames, `${when}: the row of seq ${seq} stays mounted in every frame`).toBe(0)
      expect(result!.max - result!.min, `${when}: the row of seq ${seq} moves by at most 1px in every frame`).toBeLessThanOrEqual(1)
      return result!
    },
  }
}

/** `count` paragraphs of the form `${label}${i}: ${filler}`, with a blank line between two paragraphs. */
export function markedParagraphs(label: string, count: number, filler: string): string {
  return Array.from({ length: count }, (_, i) => `${label}${i}: ${filler}`).join('\n\n')
}

/** The pattern that matches the marker at the start of a paragraph of `markedParagraphs(label, …)`. */
export function paragraphMarker(label: string): RegExp {
  return new RegExp(`^${label}\\d+:`)
}

/**
 * Send `prompt`, whose scripted answer is `answer`, and wait until the turn ends with the paragraph of `lastMarker` in
 * an assistant bubble. A multi-paragraph answer does not match `sendScriptedTurn`, which looks for the whole answer
 * as one text. Returns the step index of the answer.
 */
export async function sendLongTurn(page: Page, script: ModelScript, prompt: string, answer: string, lastMarker: string): Promise<number> {
  const step = await script.queue({ text: answer })
  await sendMessage(page, script.prompt(prompt))
  await script.waitForSteps(step + 1)
  await expect(assistantBubbles(page).filter({ hasText: lastMarker })).toHaveCount(1)
  await waitForAgentIdle(page)
  await waitForScrollRest(page)
  return step
}
