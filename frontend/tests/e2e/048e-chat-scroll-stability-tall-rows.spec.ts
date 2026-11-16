import type { Page } from '@playwright/test'
import type { PremeasureCommit, PremeasureCommits } from './helpers/chatScrollStability'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  dragChatUp,
  expectAtBottom,
  expectTailFartherBy,
  pointAtChat,
  readRowBox,
  readRowSeqWithText,
  recordPremeasureCommits,
  resizeRowsAtBottom,
  sendLongTurn,
  shortTurn,
  waitForScrollRest,
  widthSensitiveParagraphs,
} from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { chatDistanceFromBottom, chatScrollContainer, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps the content under the reader in place when the reader scrolls up into a very tall row, such as
 * a long report, on a narrow screen.
 *
 * The app measures a row that is not on screen in a hidden copy, and it holds that height until the row itself mounts
 * and measures. That moment moves nothing under the reader only while two things hold:
 *
 * - The hidden copy is exactly as wide as the row, and its height counts only at that width. In a copy that is a few
 *   pixels wider or narrower, the text wraps differently, and the height of a row of many lines is wrong by every
 *   line that wraps differently.
 * - A row that measures for the first time with the viewport top inside it keeps its bottom edge in place, so it
 *   grows above the view. A row that keeps its distance from its top edge grows at its bottom, and moves everything
 *   below the top of the view.
 *
 * Each test checks the same fact: after the reader scrolls up by N pixels, the tail of the chat is exactly N pixels
 * farther below the view. A row that grows below the top of the view pushes the tail farther away.
 *
 * Each test seeds the chat on a wide screen and reads it on a narrow one, as a reader does who opens on a phone a chat
 * that was written elsewhere. No row has a measured height at the new width, so the tall row above the view takes its
 * height from its hidden copy.
 *
 * No test here reaches the branches of the layout-shift rule (chatLayoutShift.ts) for a row whose first read in the
 * list differs from the height of its hidden copy. Such a row needs a request to the Worker (the read of an image
 * file, or the lookup of the other half of a tool call) that outlasts READINESS_REQUEST_LIMIT_MS (renderReadiness.ts)
 * and ends before the row mounts. No seam of this suite can delay one such request, so chatLayoutShift.test.ts and
 * useChatVirtualizer.geometry.test.ts cover those branches.
 */

const VIEW_HEIGHT = 600

/** The width of the viewport while the test seeds the chat: the width of the other scroll stability specs. */
const SEED_WIDTH = 1024

/**
 * The first narrow width of the viewport. From here on, the page has the layout of a narrow screen, where the chat
 * takes the whole width less a fixed inset. Its rows are wider than each row width below, so each later width
 * change drops the height of every row that is not mounted.
 */
const NARROW_WIDTH = 440

/** A width of the rows that is no multiple of 8px: a hidden copy that rounded its width would be 4px wider. */
const UNEVEN_ROW_WIDTH = 372

/** Two widths of the rows that differ by a few pixels. A height of the one is a guess at the other. */
const CLOSE_WIDE_ROW_WIDTH = 379
const CLOSE_NARROW_ROW_WIDTH = 372

/** The short turns above the tall row. */
const TURNS_ABOVE = 2

/**
 * The short turns below the tall row: about 2000px of rows on a narrow screen. At the bottom of the chat, the tall
 * row then ends about 1500px above the view, more than the 800px band of rows that the app keeps mounted there.
 */
const TURNS_BELOW = 7

/** The distance of the scroll up: it ends inside the tall row, which is far taller. */
const SCROLL_UP_PX = 2000

/** The fast scroll up, as wheel steps: the same distance. */
const FAST_STEP_PX = 200
const FAST_STEPS = SCROLL_UP_PX / FAST_STEP_PX

/**
 * The label of the paragraphs of the tall row, and their number: dozens of views of text on a narrow screen. Each
 * paragraph is one unbroken text, so the height of the row follows its width to the pixel (see
 * widthSensitiveParagraphs).
 */
const TALL_LABEL = 'Tall'
const TALL_PARAGRAPHS = 150

/** The property of `window` that holds what the watch of a row placeholder saw. */
const PLACEHOLDER_WATCH_KEY = '__chatScrollStabilityPlaceholderSeen'

/** The smallest and the largest height of a row over the frames in which it showed a placeholder, or null for none. */
interface PlaceholderHeights {
  min: number
  max: number
}

/** The tall row of a seeded chat, and the record of the heights that hidden copies commit. */
interface SeededChat {
  /** The sequence number of the tall row. */
  tallSeq: string
  premeasured: PremeasureCommits
  /** The commit of the hidden copy of the tall row at NARROW_WIDTH. */
  narrowCommit: PremeasureCommit
}

/**
 * Seed short turns, the tall row, and more short turns on a wide screen, then narrow the viewport to NARROW_WIDTH and
 * wait until the hidden copy of the tall row committed its height there.
 */
async function seedTallRow(page: Page, script: ModelScript): Promise<SeededChat> {
  await page.setViewportSize({ width: SEED_WIDTH, height: VIEW_HEIGHT })
  await waitForAgentStarted(page)
  let turn = 0
  for (let i = 0; i < TURNS_ABOVE; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  await sendLongTurn(page, script, 'Reply with a very long report.', widthSensitiveParagraphs(TALL_LABEL, TALL_PARAGRAPHS), `${TALL_LABEL}${TALL_PARAGRAPHS - 1}:`)
  const tallSeq = await readRowSeqWithText(page, `${TALL_LABEL}0:`)
  expect(tallSeq, 'the tall row is mounted at the tail').not.toBeNull()
  for (let i = 0; i < TURNS_BELOW; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  await expectAtBottom(page, 'after the seed')

  // The row measured itself at the tail, so no hidden copy committed a height for it yet.
  const premeasured = await recordPremeasureCommits(page)
  await page.setViewportSize({ width: NARROW_WIDTH, height: VIEW_HEIGHT })
  await expectAtBottom(page, 'on a narrow screen')
  await expect.poll(() => premeasured.commitOf(tallSeq!), { message: 'the hidden copy measures the tall row on a narrow screen' }).not.toBeNull()
  return { tallSeq: tallSeq!, premeasured, narrowCommit: (await premeasured.commitOf(tallSeq!))! }
}

/**
 * Wait until the hidden copy of the tall row committed a height under another height key than `previous`: the width
 * changed since. Then expect that the row is not mounted: it is far above the view. Returns the new commit.
 */
async function expectTallRowPremeasuredAgain(page: Page, chat: SeededChat, previous: PremeasureCommit): Promise<PremeasureCommit> {
  await expect.poll(async () => (await chat.premeasured.commitOf(chat.tallSeq))?.heightKey, {
    message: 'the hidden copy measures the tall row at the new width',
  }).not.toBe(previous.heightKey)
  await expectAtBottom(page, 'after the hidden copy measures the tall row')
  expect(await readRowBox(page, chat.tallSeq), 'the tall row is far above the view, so it is not mounted').toBeNull()
  return (await chat.premeasured.commitOf(chat.tallSeq))!
}

/**
 * Wait until the tall row shows its content and the chat rests, then expect that the viewport top is inside the row.
 * Returns the height of the row.
 */
async function expectViewInsideTallRow(page: Page, tallSeq: string): Promise<number> {
  await expect(page.locator(`[data-seq="${tallSeq}"]:visible`), 'the tall row shows its content').toHaveCount(1)
  await expect(page.locator('[data-testid="row-skeleton"]:visible'), 'no row placeholder remains').toHaveCount(0)
  await waitForScrollRest(page)
  const box = await readRowBox(page, tallSeq)
  expect(box, 'the tall row is mounted').not.toBeNull()
  expect(box!.distance, 'the tall row starts above the view').toBeLessThan(0)
  expect(box!.distance + box!.height, 'the tall row ends below the top of the view').toBeGreaterThan(0)
  return box!.height
}

/**
 * Record, in every animation frame from now on, the height of the row of `seq` while the row shows a placeholder in
 * place of its content. Returns a read of the record. A navigation ends the record.
 *
 * The placeholder is a direct child of the row. A row that shows its content again keeps a copy of the placeholder
 * over that content for one fade, inside a wrapper, and the record does not count it.
 */
async function watchRowPlaceholder(page: Page, seq: string): Promise<() => Promise<PlaceholderHeights | null>> {
  await chatScrollContainer(page).evaluate((container, { rowSeq, key }) => {
    Reflect.set(window, key, null)
    const sample = () => {
      if (!container.isConnected)
        return
      const row = container.querySelector(`[data-seq="${rowSeq}"]`)
      if (row !== null && row.querySelector(':scope > [data-testid="row-skeleton"]') !== null) {
        const height = row.getBoundingClientRect().height
        const seen = Reflect.get(window, key) as { min: number, max: number } | null
        Reflect.set(window, key, seen === null
          ? { min: height, max: height }
          : {
              min: Math.min(seen.min, height),
              max: Math.max(seen.max, height),
            })
      }
      requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
  }, { rowSeq: seq, key: PLACEHOLDER_WATCH_KEY })
  return () => page.evaluate(key => Reflect.get(window, key) as PlaceholderHeights | null, PLACEHOLDER_WATCH_KEY)
}

test.describe('chat scroll stability while the reader scrolls up into a very tall row', () => {
  test('keeps the rows below a very tall row in place when one scroll step ends inside the row', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsAtBottom(page, UNEVEN_ROW_WIDTH)
    const premeasured = await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)

    const before = await chatDistanceFromBottom(page)
    await dragChatUp(page, SCROLL_UP_PX)

    const height = await expectViewInsideTallRow(page, chat.tallSeq)
    await expectTailFartherBy(page, before, SCROLL_UP_PX, 'after the scroll up')
    // The hidden copy is as wide as the row, so it measured the height that the row takes.
    expect(Math.abs(height - premeasured.height), `the hidden copy measured the height of the row, within 1px (${premeasured.height}px for ${height}px)`).toBeLessThanOrEqual(1)
  })

  test('keeps the rows below a very tall row in place when a fast scroll ends inside the row', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsAtBottom(page, UNEVEN_ROW_WIDTH)
    const premeasured = await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)

    const before = await chatDistanceFromBottom(page)
    const readPlaceholder = await watchRowPlaceholder(page, chat.tallSeq)
    // A fast scroll mounts a row with a known height as a placeholder, and the row replaces it when the scroll ends.
    await pointAtChat(page)
    for (let step = 0; step < FAST_STEPS; step++)
      await page.mouse.wheel(0, -FAST_STEP_PX)

    await expectViewInsideTallRow(page, chat.tallSeq)
    const placeholder = await readPlaceholder()
    expect(placeholder, 'the tall row mounted as a placeholder during the fast scroll').not.toBeNull()
    // The placeholder fills the slot that the app keeps for the row, which has the height that the hidden copy
    // measured. A placeholder that is taller than its slot covers the top of the next row, and the edge of the row
    // moves when the content replaces it.
    for (const height of [placeholder!.min, placeholder!.max])
      expect(Math.abs(height - premeasured.height), `the placeholder is as tall as its slot, within half a pixel (${height}px for ${premeasured.height}px)`).toBeLessThanOrEqual(0.5)
    await expectTailFartherBy(page, before, SCROLL_UP_PX, 'after the scroll up')
  })

  test('measures a very tall row again when the width changes by a few pixels', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsAtBottom(page, CLOSE_WIDE_ROW_WIDTH)
    const atWideWidth = await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)
    // The narrower row wraps more lines. A height from the wider rows would be wrong by each of them, so the hidden
    // copy measures the row again under the key of the new width.
    await resizeRowsAtBottom(page, CLOSE_NARROW_ROW_WIDTH)
    const premeasured = await expectTallRowPremeasuredAgain(page, chat, atWideWidth)

    const before = await chatDistanceFromBottom(page)
    await dragChatUp(page, SCROLL_UP_PX)

    const height = await expectViewInsideTallRow(page, chat.tallSeq)
    expect(Math.abs(height - premeasured.height), `the hidden copy measured the height of the row, within 1px (${premeasured.height}px for ${height}px)`).toBeLessThanOrEqual(1)
    await expectTailFartherBy(page, before, SCROLL_UP_PX, 'after the scroll up')
  })
})
