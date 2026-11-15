import type { Page } from '@playwright/test'
import type { PremeasureCommit, PremeasureCommits } from './helpers/chatScrollStability'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  dragChatUp,
  expectAtBottom,
  pointAtChat,
  readContentBoxWidth,
  readRowBox,
  readRowHeights,
  readRowSeqWithText,
  recordPremeasureCommits,
  sendLongTurn,
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
 * - The hidden copy is exactly as wide as the row. A copy that is as wide as the list rounded to 8px is up to 4px
 *   wider or narrower. Its text then wraps differently, and the height of a row of many lines is wrong by every line
 *   that wraps differently.
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
 */

const VIEW_HEIGHT = 600

/** The width of the viewport while the test seeds the chat: the width of the other scroll stability specs. */
const SEED_WIDTH = 1024

/**
 * The first narrow width of the viewport. From here on, the page has the layout of a narrow screen, where the chat
 * takes the whole width less a fixed inset. Its rows are in another 8px step than each row width below, so each
 * later width change drops the height of every row that is not mounted.
 */
const NARROW_WIDTH = 440

/** A width of the rows that is 4px below its 8px step, 376: the largest distance between the two. */
const UNEVEN_ROW_WIDTH = 372

/** Two widths of the rows in one 8px step, 376, which holds the widths from 372 up to 380. */
const STEP_WIDE_ROW_WIDTH = 379
const STEP_NARROW_ROW_WIDTH = 372

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

/** The property of `window` that holds whether the watched row showed a placeholder. */
const PLACEHOLDER_WATCH_KEY = '__chatScrollStabilityPlaceholderSeen'

/** A short scripted turn. */
function shortTurn(turn: number) {
  return { prompt: `Turn ${turn}.`, answer: `Answer ${turn}.`, entry: 'insert' as const }
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

/** Change the width of the viewport so that each row of the chat is `rowWidth` wide, and wait for a follower at the bottom. */
async function resizeRowsTo(page: Page, rowWidth: number): Promise<void> {
  const viewport = page.viewportSize()
  expect(viewport, 'the page has a viewport').not.toBeNull()
  // The chat takes the whole width in the layout of a narrow screen, less a fixed inset.
  const inset = viewport!.width - await readContentBoxWidth(page)
  await page.setViewportSize({ width: rowWidth + inset, height: VIEW_HEIGHT })
  await expect.poll(() => readContentBoxWidth(page), { message: `each row is ${rowWidth}px wide` }).toBe(rowWidth)
  await expectAtBottom(page, `at a row width of ${rowWidth}px`)
}

/**
 * Wait until the hidden copy of the tall row committed a height under another height key than `previous`: the width
 * changed to another 8px step since. Then expect that the row is not mounted: it is far above the view. Returns the
 * new commit.
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

/** Expect that the tail of the chat is `pixels` farther below the view than `before`, within 1px. */
async function expectScrolledUpBy(page: Page, before: number, pixels: number): Promise<void> {
  const travelled = await chatDistanceFromBottom(page) - before
  expect(Math.abs(travelled - pixels), `the tail is ${pixels}px farther below the view, within 1px (it is ${travelled}px farther)`).toBeLessThanOrEqual(1)
}

/**
 * Record, in every animation frame from now on, whether the row of `seq` shows a row placeholder. Returns a read of
 * the record. A navigation ends the record.
 */
async function watchRowPlaceholder(page: Page, seq: string): Promise<() => Promise<boolean>> {
  await chatScrollContainer(page).evaluate((container, { rowSeq, key }) => {
    Reflect.set(window, key, false)
    const sample = () => {
      if (!container.isConnected)
        return
      if (container.querySelector(`[data-seq="${rowSeq}"] [data-testid="row-skeleton"]`) !== null)
        Reflect.set(window, key, true)
      requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
  }, { rowSeq: seq, key: PLACEHOLDER_WATCH_KEY })
  return () => page.evaluate(key => Reflect.get(window, key) === true, PLACEHOLDER_WATCH_KEY)
}

test.describe('chat scroll stability while the reader scrolls up into a very tall row', () => {
  test('keeps the rows below a very tall row in place when one scroll step ends inside the row', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsTo(page, UNEVEN_ROW_WIDTH)
    const premeasured = await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)

    const before = await chatDistanceFromBottom(page)
    await dragChatUp(page, SCROLL_UP_PX)

    const height = await expectViewInsideTallRow(page, chat.tallSeq)
    await expectScrolledUpBy(page, before, SCROLL_UP_PX)
    // The hidden copy is as wide as the row, so it measured the height that the row takes.
    expect(Math.abs(height - premeasured.height), `the hidden copy measured the height of the row, within 1px (${premeasured.height}px for ${height}px)`).toBeLessThanOrEqual(1)
  })

  test('keeps the rows below a very tall row in place when a fast scroll ends inside the row', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsTo(page, UNEVEN_ROW_WIDTH)
    await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)

    const before = await chatDistanceFromBottom(page)
    const sawPlaceholder = await watchRowPlaceholder(page, chat.tallSeq)
    // A fast scroll mounts a row with a known height as a placeholder, and the row replaces it when the scroll ends.
    await pointAtChat(page)
    for (let step = 0; step < FAST_STEPS; step++)
      await page.mouse.wheel(0, -FAST_STEP_PX)

    await expectViewInsideTallRow(page, chat.tallSeq)
    expect(await sawPlaceholder(), 'the tall row mounted as a placeholder during the fast scroll').toBe(true)
    await expectScrolledUpBy(page, before, SCROLL_UP_PX)
  })

  test('keeps the rows below a very tall row in place when the row corrects a height from another width', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedTallRow(page, modelScript)
    await resizeRowsTo(page, STEP_WIDE_ROW_WIDTH)
    const premeasured = await expectTallRowPremeasuredAgain(page, chat, chat.narrowCommit)
    // The two widths are in one 8px step, so the height of the hidden copy stays in use for the narrower rows.
    const heightsAtWideStep = await readRowHeights(page)
    await resizeRowsTo(page, STEP_NARROW_ROW_WIDTH)
    expect(await readRowBox(page, chat.tallSeq), 'the tall row is still not mounted').toBeNull()
    expect(await chat.premeasured.commitOf(chat.tallSeq), 'no hidden copy measured the tall row again').toEqual(premeasured)
    // Each short row keeps its height between the two widths, so the tall row alone holds a height from the other
    // width. A short row that changed its height would correct it below the reader at its mount, and move the tail.
    const heightsAtNarrowStep = await readRowHeights(page)
    for (const [seq, height] of Object.entries(heightsAtWideStep)) {
      const narrow = heightsAtNarrowStep[seq]
      if (narrow !== undefined)
        expect(Math.abs(narrow - height), `the row of seq ${seq} keeps its height between the two widths`).toBeLessThanOrEqual(0.5)
    }

    const before = await chatDistanceFromBottom(page)
    await dragChatUp(page, SCROLL_UP_PX)

    const height = await expectViewInsideTallRow(page, chat.tallSeq)
    // The narrower row wraps more lines, so the row corrects its height when it mounts. The test needs that
    // correction: without it, the row keeps its height and nothing can move.
    expect(Math.abs(height - premeasured.height), `the tall row corrected the height from the other width (${premeasured.height}px to ${height}px)`).toBeGreaterThan(1)
    await expectScrolledUpBy(page, before, SCROLL_UP_PX)
  })
})
