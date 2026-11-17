import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ScriptedTurn } from './scriptedTurn'
import { expect } from '@playwright/test'
import { parseStoredKey, PREFIX_CHAT_ROW_HEIGHTS } from '../../../src/lib/browserStorage'
import { escapeRegExp } from '../../../src/lib/regexp'
import { deleteEntries, storageKeys } from './storage'
import { assistantBubbles, CHAT_SCROLL_CONTAINER, chatDistanceFromBottom, chatScrollContainer, sendMessage, waitForAgentIdle } from './ui'

/**
 * Reads and actions for the specs that check that the chat view keeps what the reader sees in place
 * (`048*-chat-scroll-stability*.spec.ts` and `codex/chat-scroll-stability-*.spec.ts`).
 *
 * Each read of a row runs in ONE `evaluate` on the scroll container, which the app never rebuilds. A read through a
 * chat locator could hold a row that ChatView replaced between the resolve and the read (see `readAttached`). The
 * functions that run in the browser (each one whose name ends in `In`) reference nothing outside their own body,
 * because Playwright sends only their source text to the page.
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

/** A row of the chat: the distance from the top edge of the scroll container to its top edge, and its height. */
export interface RowBox {
  distance: number
  height: number
}

/** The box of the row of `seq` in `container`, or null when the row is not mounted. Runs in the browser. */
export function rowBoxIn(container: Element, seq: string): RowBox | null {
  const row = container.querySelector(`[data-seq="${seq}"]`)
  if (row === null)
    return null
  const rect = row.getBoundingClientRect()
  return { distance: rect.top - container.getBoundingClientRect().top, height: rect.height }
}

/**
 * The sequence number of the first mounted row of `container`, in document order, whose text contains `text`, or null
 * when no mounted row contains it. Runs in the browser.
 */
export function rowSeqWithTextIn(container: Element, text: string): string | null {
  const row = Array.from(container.querySelectorAll<HTMLElement>('[data-seq]')).find(candidate => candidate.textContent?.includes(text))
  return row?.dataset.seq ?? null
}

/**
 * The width of the content box of `container`: its client width without its left and right padding. Each row of the
 * chat is that wide. Runs in the browser.
 */
export function contentBoxWidthIn(container: Element): number {
  const style = getComputedStyle(container)
  return container.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight)
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

/** Read the box of the row of `seq` (see rowBoxIn). */
export function readRowBox(page: Page, seq: string): Promise<RowBox | null> {
  return chatScrollContainer(page).evaluate(rowBoxIn, seq)
}

/** Read the sequence number of the mounted row whose text contains `text` (see rowSeqWithTextIn). */
export function readRowSeqWithText(page: Page, text: string): Promise<string | null> {
  return chatScrollContainer(page).evaluate(rowSeqWithTextIn, text)
}

/** Read the width of the content box of the chat, which is the width of each row (see contentBoxWidthIn). */
export function readContentBoxWidth(page: Page): Promise<number> {
  return chatScrollContainer(page).evaluate(contentBoxWidthIn)
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

/**
 * Resolve when the page drew `count` animation frames from now, and at least one. Runs in the browser.
 */
export function framesPassIn(count: number): Promise<void> {
  return new Promise<void>((resolve) => {
    let seen = 0
    const tick = () => {
      seen++
      if (seen >= count)
        resolve()
      else
        requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
}

/** Wait until the page drew `frames` animation frames (see framesPassIn). */
export function framesPass(page: Page, frames: number): Promise<void> {
  return page.evaluate(framesPassIn, frames)
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

/**
 * Move the view up by `pixels` in one step, as one move of a drag of the scrollbar thumb does, then wait until the
 * chat stops scrolling. The whole distance arrives in one `scroll` event, whatever the platform does with a wheel.
 * A row that was not mounted before the step therefore mounts with the viewport already at its end position.
 */
export async function dragChatUp(page: Page, pixels: number): Promise<void> {
  await chatScrollContainer(page).evaluate((container, distance) => {
    container.scrollTop -= distance
  }, pixels)
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

/**
 * Expect that the tail of the chat is `pixels` farther below the view than `before`, within 1px. `before` is a read of
 * `chatDistanceFromBottom`. A scroll up by N pixels moves the tail N pixels farther below the view. A row that grows
 * below the top of the view moves the tail farther still.
 */
export async function expectTailFartherBy(page: Page, before: number, pixels: number, when: string): Promise<void> {
  const travelled = await chatDistanceFromBottom(page) - before
  expect(Math.abs(travelled - pixels), `${when}: the tail is ${pixels}px farther below the view, within 1px (it is ${travelled}px farther)`).toBeLessThanOrEqual(1)
}

/**
 * Change the width of the viewport so that each row of the chat is `rowWidth` wide, and wait until the chat has that
 * width. The viewport keeps its height.
 *
 * The page must have the layout of a narrow screen, where the chat takes the whole width less a fixed inset.
 */
export async function resizeRowsTo(page: Page, rowWidth: number): Promise<void> {
  const viewport = page.viewportSize()
  expect(viewport, 'the page has a viewport').not.toBeNull()
  const inset = viewport!.width - await readContentBoxWidth(page)
  await page.setViewportSize({ width: rowWidth + inset, height: viewport!.height })
  await expect.poll(() => readContentBoxWidth(page), { message: `each row is ${rowWidth}px wide` }).toBe(rowWidth)
}

/** Resize the rows to `rowWidth` (see resizeRowsTo), then expect that the view rests at the bottom of the chat. */
export async function resizeRowsAtBottom(page: Page, rowWidth: number): Promise<void> {
  await resizeRowsTo(page, rowWidth)
  await expectAtBottom(page, `at a row width of ${rowWidth}px`)
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

/** A row of the chat in one animation frame, as the frame watch samples it. */
export interface WatchedRow {
  seq: string
  /** The distance from the top edge of the scroll container to the top edge of the row. */
  top: number
  height: number
  /**
   * The identity of the element of the row: one number for each element, for the life of the watch. A row that mounts
   * again has another element, and so another number.
   */
  element: number
  /** Whether the row shows. A row that waits for its content (`visibility: hidden`) shows nothing. */
  shown: boolean
}

/** The rows that one animation frame held in the view: each mounted row that reaches into it. */
export type WatchedFrame = readonly WatchedRow[]

/** A change in the view that no scroll makes. */
export interface FrameJump {
  /** The index of the frame that shows the change, in the order of the watch. */
  frame: number
  /**
   * - `remount`: a row in the view mounted again: it had another element a few frames before (see
   *   REMOUNT_WINDOW_FRAMES). The reader sees the row vanish and come back, or turn into its skeleton.
   * - `height`: a row that shows in both of two consecutive frames changed its height.
   * - `shear`: two rows that show in both frames moved by distances that differ, so a part of the view moved against
   *   the rest of it.
   */
  kind: 'remount' | 'height' | 'shear'
  /** The rows and the distances, for the failure message. */
  detail: string
}

/**
 * The most frames between two samples of a row in the view in which a new element of the row counts as a `remount`.
 * The rendered rows reach past the view by the overscan, so a row that scrolls out of the rendered rows and back takes
 * far more frames than this. A row that unmounts and mounts again within one task shows a new element in the next
 * frame. One that unmounts in one task and mounts again in a later one can leave a frame or two with no row.
 */
export const REMOUNT_WINDOW_FRAMES = 3

/**
 * The jumps in `frames`. A scroll moves every row on screen by one distance, changes no height, and keeps every
 * element. So in a transcript that does not change:
 *
 * - Each row in the view keeps its element. A row with a new element within REMOUNT_WINDOW_FRAMES frames of its last
 *   sample mounted again. It counts whether it shows or waits for its content.
 * - Each row that shows in two consecutive frames keeps its height, within `tolerancePx`, and all of those rows move
 *   by the same distance, within `tolerancePx`. A row that only one of the two frames shows, because it mounted,
 *   unmounted, scrolled into the view or waits for its content, counts for nothing here.
 *
 * The check needs no knowledge of the input: a wheel, a drag, a fling, and a rebase of the layout shift each move
 * every row by one distance. A row that changes its height in view moves the rows on one side of it and not the rows
 * on the other side, and the row itself.
 */
export function frameJumps(frames: readonly WatchedFrame[], tolerancePx = 1): FrameJump[] {
  // A length in a message, rounded to a hundredth of a pixel: a difference of two layout positions carries the error
  // of the floating point sum.
  const px = (length: number): string => `${Number(length.toFixed(2))}px`
  const jumps: FrameJump[] = []
  // The last sample of each row: the index of its frame, and its element.
  const lastSample = new Map<string, { frame: number, element: number }>()
  for (let frame = 0; frame < frames.length; frame++) {
    const rows = frames[frame]!
    for (const row of rows) {
      const last = lastSample.get(row.seq)
      if (last !== undefined && last.element !== row.element && frame - last.frame <= REMOUNT_WINDOW_FRAMES)
        jumps.push({ frame, kind: 'remount', detail: `the row of seq ${row.seq} mounted again ${frame - last.frame} frame(s) after its last sample` })
      lastSample.set(row.seq, { frame, element: row.element })
    }
    if (frame === 0)
      continue
    const before = new Map(frames[frame - 1]!.filter(row => row.shown).map(row => [row.seq, row]))
    const moves: { seq: string, distance: number }[] = []
    for (const row of rows) {
      const earlier = before.get(row.seq)
      if (earlier === undefined || !row.shown)
        continue
      if (Math.abs(row.height - earlier.height) > tolerancePx)
        jumps.push({ frame, kind: 'height', detail: `the row of seq ${row.seq} changed its height from ${px(earlier.height)} to ${px(row.height)}` })
      moves.push({ seq: row.seq, distance: row.top - earlier.top })
    }
    if (moves.length < 2)
      continue
    const least = moves.reduce((a, b) => (b.distance < a.distance
      ? b
      : a))
    const most = moves.reduce((a, b) => (b.distance > a.distance
      ? b
      : a))
    if (most.distance - least.distance > tolerancePx)
      jumps.push({ frame, kind: 'shear', detail: `the row of seq ${least.seq} moved ${px(least.distance)} and the row of seq ${most.seq} moved ${px(most.distance)}` })
  }
  return jumps
}

/** The state of a running frame watch, which the page keeps on `window` under the key of the watch. */
interface FrameWatchState {
  frames: WatchedFrame[]
  stopped: boolean
}

/** The property of `window` that holds the state of the running frame watch. One frame watch runs at a time. */
const FRAME_WATCH_KEY = '__chatScrollStabilityFrameWatch'

/**
 * Sample, in every animation frame, each mounted row of `container` that reaches into the view, and keep the frames on
 * `window` under `key`. Each sample states the element of the row, and whether the row shows: a row that waits for its
 * content (`visibility: hidden`) shows nothing. Runs in the browser.
 */
export function startFrameWatchIn(container: Element, key: string): void {
  const state: FrameWatchState = { frames: [], stopped: false }
  Reflect.set(window, key, state)
  // A number for each row element that the watch samples, so a row that mounts again shows as another element.
  const elementIds = new WeakMap<Element, number>()
  let nextElementId = 0
  const sample = () => {
    if (state.stopped)
      return
    const top = container.getBoundingClientRect().top
    const bottom = top + container.clientHeight
    const rows: WatchedRow[] = []
    for (const row of Array.from(container.querySelectorAll<HTMLElement>('[data-seq]'))) {
      const rect = row.getBoundingClientRect()
      const seq = row.dataset.seq
      if (seq === undefined || rect.bottom <= top || rect.top >= bottom)
        continue
      let element = elementIds.get(row)
      if (element === undefined) {
        element = ++nextElementId
        elementIds.set(row, element)
      }
      rows.push({
        seq,
        top: rect.top - top,
        height: rect.height,
        element,
        shown: row.style.visibility !== 'hidden',
      })
    }
    state.frames.push(rows)
    requestAnimationFrame(sample)
  }
  requestAnimationFrame(sample)
}

/** Stop the frame watch under `key`, and return its frames, or null when no watch runs under it. Runs in the browser. */
export function stopFrameWatchIn(key: string): WatchedFrame[] | null {
  const state = Reflect.get(window, key) as FrameWatchState | undefined
  if (state === undefined)
    return null
  state.stopped = true
  Reflect.deleteProperty(window, key)
  return state.frames
}

/** The frame watch that `watchFrames` starts. */
export interface FrameWatch {
  /**
   * Stop the watch, and expect that it sampled frames and that no two consecutive frames show a jump (see
   * frameJumps).
   */
  expectNoJump: (when: string) => Promise<void>
}

/**
 * Sample every row in the view in every animation frame, until the caller stops the watch, and check that the rows
 * only moved together, as a scroll moves them, and kept their elements (see frameJumps). A check at rest sees only the
 * end state, and a check of one row sees no row that changes its height below it. A check of a row by its sequence
 * number sees no row that mounts again.
 */
export async function watchFrames(page: Page): Promise<FrameWatch> {
  await chatScrollContainer(page).evaluate(startFrameWatchIn, FRAME_WATCH_KEY)
  return {
    expectNoJump: async (when) => {
      const frames = await page.evaluate(stopFrameWatchIn, FRAME_WATCH_KEY)
      expect(frames, `${when}: the frame watch ran`).not.toBeNull()
      expect(frames!.length, `${when}: the frame watch sampled frames`).toBeGreaterThan(1)
      expect(frameJumps(frames!), `${when}: the rows in view keep their elements and only move together`).toEqual([])
    },
  }
}

/** `count` paragraphs of the form `${label}${i}: ${filler}`, with a blank line between two paragraphs. */
export function markedParagraphs(label: string, count: number, filler: string): string {
  return Array.from({ length: count }, (_, i) => `${label}${i}: ${filler}`).join('\n\n')
}

/**
 * The pattern that matches the marker at the start of a paragraph of `markedParagraphs(label, …)`. It matches each
 * character of `label` literally, also a character with a meaning in a regular expression.
 */
export function paragraphMarker(label: string): RegExp {
  return new RegExp(`^${escapeRegExp(label)}\\d+:`)
}

/** The characters of the unbroken texts of `widthSensitiveParagraphs`: glyphs of many widths in a proportional font. */
const UNBROKEN_TEXT_CHARACTERS = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'

/**
 * `count` paragraphs of the form `${label}${i}: ${text}`, with a blank line between two paragraphs. Each text is one
 * unbroken run of 200 to 699 characters, with its own length and its own characters, as a long hash or an encoded
 * value is.
 *
 * The chat breaks such a run at any character, so the run fills each line, and its number of lines is its width
 * divided by the width of the line. A paragraph of ten lines therefore gains or loses a line for one width change in
 * ten of 1%, which is 4px on a narrow screen. Among many paragraphs of different lengths, some change their number of
 * lines for a change of the width by a few pixels, whatever the font is. Prose does not: a paragraph of words keeps
 * its number of lines until a whole word fits on another line.
 */
export function widthSensitiveParagraphs(label: string, count: number): string {
  return Array.from({ length: count }, (_, i) => {
    const text = Array.from(
      { length: 200 + (i * 37) % 500 },
      (_, c) => UNBROKEN_TEXT_CHARACTERS[(i * 7 + c * (17 + (i % 3) * 4) + (c * c) % 7) % UNBROKEN_TEXT_CHARACTERS.length],
    ).join('')
    return `${label}${i}: ${text}`
  }).join('\n\n')
}

/** The height that the hidden premeasure copy of a row committed, and the height key of the row at that commit. */
export interface PremeasureCommit {
  height: number
  /**
   * The height key holds the exact row width and the fonts of the chat, so a commit at another width has another key.
   */
  heightKey: string
}

/** The property of `window` that holds the commits that `recordPremeasureCommits` collects. */
const PREMEASURE_COMMITS_KEY = '__chatScrollStabilityPremeasureCommits'

/**
 * Collect, on `window` under `key`, the last height that the hidden premeasure copy of each row commits, by the
 * sequence number of the row. The app reports each commit as a `leapmux:chat-premeasure` event in a development
 * build, which the E2E launcher builds. Runs in the browser.
 */
export function startPremeasureRecordIn(key: string): void {
  const commits: Record<string, PremeasureCommit> = {}
  Reflect.set(window, key, commits)
  window.addEventListener('leapmux:chat-premeasure', (event) => {
    const detail: unknown = event instanceof CustomEvent
      ? event.detail
      : undefined
    if (typeof detail !== 'object' || detail === null)
      return
    const { phase, accepted, seq, height, heightKey } = detail as Record<string, unknown>
    if (phase === 'commit' && accepted === true && typeof seq === 'string' && typeof height === 'number' && typeof heightKey === 'string')
      commits[seq] = { height, heightKey }
  })
}

/** The commit that the record under `key` holds for the row of `seq`, or null. Runs in the browser. */
export function premeasureCommitIn({ key, seq }: { key: string, seq: string }): PremeasureCommit | null {
  const commits = Reflect.get(window, key) as Record<string, PremeasureCommit> | undefined
  return commits?.[seq] ?? null
}

/** The record that `recordPremeasureCommits` starts. */
export interface PremeasureCommits {
  /** The last commit of the hidden copy of the row of `seq` since the record started, or null. */
  commitOf: (seq: string) => Promise<PremeasureCommit | null>
}

/**
 * Record the height that the hidden premeasure copy of each row commits from now on (see startPremeasureRecordIn).
 * A navigation ends the record, so the next test starts with none.
 */
export async function recordPremeasureCommits(page: Page): Promise<PremeasureCommits> {
  await page.evaluate(startPremeasureRecordIn, PREMEASURE_COMMITS_KEY)
  return {
    commitOf: seq => page.evaluate(premeasureCommitIn, { key: PREMEASURE_COMMITS_KEY, seq }),
  }
}

/**
 * Record the height that the hidden premeasure copy of each row commits, from the next load of the page on (see
 * startPremeasureRecordIn). Use it for a commit that can come before a call in the loaded page could start the
 * record, as right after the chat opens.
 *
 * It adds an init script to the page and hands its `Disposable` to no one. The reset of the shared page cannot
 * dispose an init script whose `Disposable` it does not hold, so a spec that calls this helper runs in a context of its
 * own (ISOLATED_CONTEXT_SPECS in fixtures.ts).
 */
export async function recordPremeasureCommitsFromNextLoad(page: Page): Promise<PremeasureCommits> {
  await page.addInitScript(startPremeasureRecordIn, PREMEASURE_COMMITS_KEY)
  return {
    commitOf: seq => page.evaluate(premeasureCommitIn, { key: PREMEASURE_COMMITS_KEY, seq }),
  }
}

/**
 * A commit that changed the height of a row, as a development build of the app reports it in a
 * `leapmux:chat-row-height` event (see RowHeightCache.commit in the app).
 */
export interface RowHeightChange {
  /** The sequence number of the row, or null for a row with none. */
  seq: string | null
  /** What read the height: the row in the list (`visible`), or a hidden copy or the storage (`premeasure`). */
  source: 'visible' | 'premeasure'
  /** Whether the row was mounted at the commit. */
  mounted: boolean
  /** The height that the app held for the row before the commit, or null for a row with none. */
  previousHeight: number | null
  height: number
  /** The height key of the height before the commit, or null. */
  previousKey: string | null
  heightKey: string | null
}

/** The property of `window` that holds the changes that `recordRowHeightChangesFromNextLoad` collects. */
const ROW_HEIGHT_CHANGES_KEY = '__chatScrollStabilityRowHeightChanges'

/**
 * Collect, on `window` under `key`, each `leapmux:chat-row-height` event of the page, in the order of its arrival.
 * Runs in the browser.
 */
export function startRowHeightRecordIn(key: string): void {
  const changes: RowHeightChange[] = []
  Reflect.set(window, key, changes)
  window.addEventListener('leapmux:chat-row-height', (event) => {
    const detail: unknown = event instanceof CustomEvent
      ? event.detail
      : undefined
    if (typeof detail !== 'object' || detail === null)
      return
    const { seq, source, mounted, previousHeight, height, previousKey, heightKey } = detail as Record<string, unknown>
    if ((source !== 'visible' && source !== 'premeasure') || typeof height !== 'number')
      return
    changes.push({
      seq: typeof seq === 'string'
        ? seq
        : null,
      source,
      mounted: mounted === true,
      previousHeight: typeof previousHeight === 'number'
        ? previousHeight
        : null,
      height,
      previousKey: typeof previousKey === 'string'
        ? previousKey
        : null,
      heightKey: typeof heightKey === 'string'
        ? heightKey
        : null,
    })
  })
}

/** The changes that the record under `key` collected, or an empty list when no record runs. Runs in the browser. */
export function rowHeightChangesIn(key: string): RowHeightChange[] {
  return (Reflect.get(window, key) as RowHeightChange[] | undefined) ?? []
}

/** The record that `recordRowHeightChangesFromNextLoad` starts. */
export interface RowHeightChanges {
  /** The changes since the record started, in the order of their commits. */
  all: () => Promise<RowHeightChange[]>
}

/**
 * Record each commit that changes the height of a row from now on (see startRowHeightRecordIn). In a transcript that
 * does not change, a commit of a mounted row (`source: 'visible'`) changed a height that the reader could see, so a
 * spec expects none while it scrolls through history. A navigation ends the record.
 */
export async function recordRowHeightChanges(page: Page): Promise<RowHeightChanges> {
  await page.evaluate(startRowHeightRecordIn, ROW_HEIGHT_CHANGES_KEY)
  return {
    all: () => page.evaluate(rowHeightChangesIn, ROW_HEIGHT_CHANGES_KEY),
  }
}

/**
 * Record each commit that changes the height of a row, from the next load of the page on (see recordRowHeightChanges).
 * Use it for a commit that can come before a call in the loaded page could start the record, as right after the chat
 * opens.
 *
 * It adds an init script to the page and hands its `Disposable` to no one, so only a spec with a page of its own may
 * call it (see recordPremeasureCommitsFromNextLoad).
 */
export async function recordRowHeightChangesFromNextLoad(page: Page): Promise<RowHeightChanges> {
  await page.addInitScript(startRowHeightRecordIn, ROW_HEIGHT_CHANGES_KEY)
  return {
    all: () => page.evaluate(rowHeightChangesIn, ROW_HEIGHT_CHANGES_KEY),
  }
}

/** Expect that no commit of `changes` changed the height of a mounted row: the reader saw no row change its height. */
export async function expectNoVisibleHeightChange(changes: RowHeightChanges, when: string): Promise<void> {
  expect((await changes.all()).filter(change => change.source === 'visible'), `${when}: no row changes its height where the reader can see it`).toEqual([])
}

/** A short scripted turn. */
export function shortTurn(turn: number): ScriptedTurn {
  return { prompt: `Turn ${turn}.`, answer: `Answer ${turn}.`, entry: 'insert' }
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

/** A mounted row of the chat, as `rowStatesIn` reads it. */
export interface RowState {
  seq: string
  /** The distance from the top edge of the scroll container to the top edge of the row. */
  distance: number
  height: number
}

/** The state of each mounted row of `container`, in document order. Runs in the browser. */
export function rowStatesIn(container: Element): RowState[] {
  const top = container.getBoundingClientRect().top
  return Array.from(container.querySelectorAll<HTMLElement>('[data-seq]')).map((row) => {
    const rect = row.getBoundingClientRect()
    return {
      seq: row.dataset.seq ?? '',
      distance: rect.top - top,
      height: rect.height,
    }
  })
}

/** Read the state of each mounted row (see rowStatesIn). */
export function readRowStates(page: Page): Promise<RowState[]> {
  return chatScrollContainer(page).evaluate(rowStatesIn)
}

/** The sequence numbers of the mounted rows of `container`, in document order. Runs in the browser. */
export function mountedRowSeqsIn(container: Element): number[] {
  const seqs: number[] = []
  for (const row of Array.from(container.querySelectorAll<HTMLElement>('[data-seq]'))) {
    if (row.dataset.seq !== undefined)
      seqs.push(Number(row.dataset.seq))
  }
  return seqs
}

/** Read the sequence numbers of the mounted rows, in order (see mountedRowSeqsIn). */
export function readMountedRowSeqs(page: Page): Promise<number[]> {
  return chatScrollContainer(page).evaluate(mountedRowSeqsIn)
}

/** The container of the one chat on screen. The shell hides the chat of an inactive tab, and that chat does not match. */
const VISIBLE_CHAT_CONTAINER = '[data-testid="chat-container"]:visible'

/**
 * The loading indicator of the chat on screen whose text is exactly `text`, such as "Loading older messages...". The
 * indicators are overlays beside the scroll container, not inside it, so the locator starts at the container of the
 * chat.
 *
 * An element matches when its whole text equals `text`, and no child element of it matches too. The whole text holds
 * the text of each descendant, and Playwright trims it and counts each run of white space in it as one space. The
 * Spinner icon of an indicator has no text, so the `div` that holds the icon and the text matches. A row that quotes
 * the text inside a longer one does not match.
 */
export function loadingIndicator(page: Page, text: string): Locator {
  return page.locator(VISIBLE_CHAT_CONTAINER).getByText(text, { exact: true })
}

/** The hold that `installReplyHoldIn` keeps on `window`. */
interface ReplyHold {
  /** The text that marks a reply to hold, or null once the hold ended. */
  marker: string | null
  /** The deliveries of the held replies, in the order of their arrival. */
  held: (() => void)[]
}

/**
 * Hold back each worker reply whose rendered HTML contains `marker`, and keep the hold on `window` under `key`. A
 * reply is a message with an `html` text, as the Markdown worker sends. The row of that text then stays behind its
 * placeholder, as a long report does on a slow device.
 *
 * It replaces the `onmessage` accessor of `Worker.prototype`, so it holds only for a worker whose handler the page
 * sets after this function runs. Run it before the scripts of the page. Runs in the browser.
 */
export function installReplyHoldIn({ key, marker }: { key: string, marker: string }): void {
  const descriptor = Object.getOwnPropertyDescriptor(Worker.prototype, 'onmessage')
  const get = descriptor?.get
  const set = descriptor?.set
  if (get === undefined || set === undefined)
    throw new Error('Worker.prototype.onmessage is not an accessor')
  const hold: ReplyHold = { marker, held: [] }
  Reflect.set(window, key, hold)
  Object.defineProperty(Worker.prototype, 'onmessage', {
    configurable: true,
    enumerable: descriptor?.enumerable ?? true,
    get(this: Worker) {
      return get.call(this)
    },
    set(this: Worker, handler: unknown) {
      if (typeof handler !== 'function') {
        set.call(this, handler)
        return
      }
      set.call(this, (event: MessageEvent) => {
        const html = (event.data as { html?: unknown } | null)?.html
        if (hold.marker !== null && typeof html === 'string' && html.includes(hold.marker)) {
          hold.held.push(() => handler.call(this, event))
          return
        }
        handler.call(this, event)
      })
    },
  })
}

/** The number of replies that the hold under `key` keeps back, or 0 when no hold exists. Runs in the browser. */
export function heldReplyCountIn(key: string): number {
  const hold = Reflect.get(window, key) as ReplyHold | undefined
  return hold?.held.length ?? 0
}

/**
 * Deliver the replies that the hold under `key` keeps back, in the order of their arrival, and end the hold, so it
 * keeps back no later reply. Does nothing when no hold exists. Runs in the browser.
 */
export function releaseHeldRepliesIn(key: string): void {
  const hold = Reflect.get(window, key) as ReplyHold | undefined
  if (hold === undefined)
    return
  hold.marker = null
  for (const deliver of hold.held.splice(0))
    deliver()
}

/** The property of `window` that holds the hold of `holdRenderRepliesWith`. */
const REPLY_HOLD_KEY = '__chatScrollStabilityReplyHold'

/** The hold that `holdRenderRepliesWith` starts. */
export interface HeldRenderReplies {
  /** The number of replies that the hold keeps back now. */
  heldCount: () => Promise<number>
  /** Deliver the held replies and end the hold (see releaseHeldRepliesIn). */
  release: () => Promise<void>
}

/**
 * From the next navigation of `page` on, hold back each worker reply whose rendered HTML contains `marker` (see
 * installReplyHoldIn), until `release`.
 *
 * It adds an init script to the page and hands its `Disposable` to no one. The reset of the shared page cannot
 * dispose an init script whose `Disposable` it does not hold. So only a spec with a page of its own may call this
 * helper: list the spec in ISOLATED_CONTEXT_SPECS (see fixtures.ts).
 *
 * The app ends a render that takes longer than its time budget, and then shows the source of the text. The budget
 * of a held reply runs from the moment its request is the oldest one of its worker without a reply:
 *
 * - A body of ordinary size shares its worker with every other row, and its budget is about 10 seconds (see
 *   markdownRenderBudgetMs). A test that scrolls for a few seconds under load does not finish in that time.
 * - A body above 32 KiB renders in a worker of its own (see LARGE_MARKDOWN_BODY_CHARS). The held reply is then the
 *   first answer that this worker owes, and the app waits 30 seconds longer for the first answer of a worker (see
 *   WORKER_COLD_START_ALLOWANCE_MS). Give the held body that size with `largeBodyFiller`.
 *
 * Check that the row still waits right before the release.
 */
export async function holdRenderRepliesWith(page: Page, marker: string): Promise<HeldRenderReplies> {
  await page.addInitScript(installReplyHoldIn, { key: REPLY_HOLD_KEY, marker })
  return {
    heldCount: () => page.evaluate(heldReplyCountIn, REPLY_HOLD_KEY),
    release: () => page.evaluate(releaseHeldRepliesIn, REPLY_HOLD_KEY),
  }
}

/**
 * The length of the address in the link definition of `largeBodyFiller`: above the 32 KiB at which the app renders a
 * Markdown body in a worker of its own (LARGE_MARKDOWN_BODY_CHARS, which the test of this file compares it with).
 */
export const LARGE_BODY_FILLER_CHARS = 40 * 1024

/**
 * A Markdown block that makes a body large and draws nothing: one link definition that no link uses, with an address
 * of LARGE_BODY_FILLER_CHARS characters. Append it to a body after a blank line. The height of the row stays the
 * height of the rest of the body.
 */
export function largeBodyFiller(): string {
  return `[large-body-filler]: https://example.invalid/${'f'.repeat(LARGE_BODY_FILLER_CHARS)}`
}

/**
 * The database in which the app keeps each rendered Markdown body and each highlighted code block for the next load
 * of the page, and its object store (see renderArtifactStore.ts in the app).
 */
const STORED_RENDERS = { database: 'leapmux-render-cache', store: 'artifacts' } as const

/**
 * Delete every entry of the object store `store` of the IndexedDB database `database`. Rejects when the database
 * has no such store, so a renamed store fails the caller. Runs in the browser.
 */
export function clearObjectStoreIn({ database, store }: { database: string, store: string }): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const open = indexedDB.open(database)
    open.onerror = () => reject(open.error ?? new Error(`cannot open the database ${database}`))
    open.onsuccess = () => {
      const db = open.result
      if (!db.objectStoreNames.contains(store)) {
        db.close()
        reject(new Error(`the database ${database} has no object store ${store}`))
        return
      }
      const transaction = db.transaction(store, 'readwrite')
      transaction.objectStore(store).clear()
      transaction.oncomplete = () => {
        db.close()
        resolve()
      }
      transaction.onerror = () => {
        db.close()
        reject(transaction.error ?? new Error(`cannot clear the object store ${store}`))
      }
    }
  })
}

/**
 * Delete each render that the app keeps for the next load of the page. After a reload, the page then renders each
 * row again in its worker, as a browser does that shows the chat for the first time.
 */
export function clearStoredRenders(page: Page): Promise<void> {
  return page.evaluate(clearObjectStoreIn, STORED_RENDERS)
}

/** The path of the empty page on which `clearStoredRowHeights` leaves the browser. */
const BLANK_PAGE_PATH = '/__e2e-blank'

/**
 * Whether the stored key `storedKey` holds the row heights of a chat. The app's own parser reads the logical name of
 * the key, so this check follows each change of the key layout.
 */
function holdsRowHeights(storedKey: string): boolean {
  return parseStoredKey(storedKey)?.name.startsWith(PREFIX_CHAT_ROW_HEIGHTS) === true
}

/** The stored keys that hold the row heights of a chat. */
async function rowHeightKeys(page: Page): Promise<string[]> {
  return (await storageKeys(page)).filter(holdsRowHeights)
}

/**
 * Delete the row heights that the app stores for each chat, and leave the browser on an empty page. The caller
 * opens the chat again. No row then has a stored height, as in a browser that shows the chat for the first time.
 *
 * The app writes the heights that it still owes when its page goes away (`pagehide`), and that write would follow a
 * delete that runs inside the app. So this helper leaves the app first: it goes to an empty page of the same origin,
 * and deletes the entries there. It deletes no other entry.
 *
 * The helper fails when it deletes no entry, because a broken match of the stored keys also deletes none. The app
 * saves the heights one second after the last height that it measured (ROW_HEIGHT_SAVE_DEBOUNCE_MS in
 * chatRowHeightPersistence.ts), and it drops a save that still waits when its page goes away. So the helper first
 * waits until the app saved the heights.
 */
export async function clearStoredRowHeights(page: Page): Promise<void> {
  await expect.poll(async () => (await rowHeightKeys(page)).length, {
    message: 'the app stores the row heights of the chat',
  }).toBeGreaterThan(0)
  const blank = new URL(BLANK_PAGE_PATH, page.url()).toString()
  await page.route(blank, route => route.fulfill({ status: 200, contentType: 'text/html', body: '' }))
  try {
    await page.goto(blank)
  }
  finally {
    await page.unroute(blank)
  }
  expect(await deleteEntries(page, await rowHeightKeys(page)), 'the helper deletes the stored row heights').toBeGreaterThan(0)
}
