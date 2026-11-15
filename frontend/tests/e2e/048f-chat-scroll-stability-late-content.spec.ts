import type { Page } from '@playwright/test'
import type { HeldRenderReplies, RowState } from './helpers/chatScrollStability'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  clearStoredRenders,
  dragChatUp,
  expectAtBottom,
  holdRenderRepliesWith,
  largeBodyFiller,
  readContentBoxWidth,
  readRowBox,
  readRowSeqWithText,
  readRowStates,
  sendLongTurn,
  waitForScrollRest,
  widthSensitiveParagraphs,
} from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { assistantBubbles, chatDistanceFromBottom, chatScrollContainer, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps the rows that the reader looks at in place when the content of a tall row arrives late.
 *
 * A worker renders the Markdown of a row, and a row waits behind a placeholder until its render arrives. The app does
 * not know the height of such a row, so the placeholder has the usual height of a short row. A long report takes the
 * longest to render, and a slow device takes longer still, so the reader can scroll up until the placeholder is in
 * the view. The row then takes its real height there, which is several views.
 *
 * - With the placeholder high in the view, most of what the reader sees lies below it. The row then grows above
 *   itself, and the rows below it stay where they are. A row that grows at its bottom there pushes them out of the
 *   view.
 * - With the placeholder low in the view, most of what the reader sees lies above it. The row then grows at its
 *   bottom, and the rows above it stay where they are.
 *
 * Each test seeds the chat, then loads it again as a browser does that never rendered its rows, and holds back the
 * render of the tall row until the reader scrolled up to the placeholder.
 *
 * The source of the tall row ends with a large link definition that draws nothing (see largeBodyFiller). The app then
 * renders the row in a worker of its own and waits about 40 seconds for it, where it waits 10 seconds for a row of
 * ordinary size (see holdRenderRepliesWith). So the hold outlasts the scroll of the test, also under load.
 */

const VIEW_HEIGHT = 600

/** The width of the viewport while the test seeds the chat: the width of the other scroll stability specs. */
const SEED_WIDTH = 1024

/**
 * The first narrow width of the viewport, at which the test reads the inset of the chat and loads the page again. Its
 * rows are in another 8px step than ROW_WIDTH, so no row has a stored height at ROW_WIDTH.
 */
const NARROW_WIDTH = 440

/** The width of the rows while the reader scrolls. */
const ROW_WIDTH = 372

/** The short turns above the tall row. */
const TURNS_ABOVE = 2

/**
 * The short turns below the tall row: about 2300px of rows on a narrow screen. At the bottom of the chat, the tall
 * row then lies about 1800px above the view, more than the 800px band of rows that the app keeps mounted there.
 */
const TURNS_BELOW = 8

/** The label of the paragraphs of the tall row (see widthSensitiveParagraphs). */
const LATE_LABEL = 'Late'

/**
 * The scroll steps toward the tall row, and the most steps of each kind that the reader needs.
 *
 * The app keeps a row hidden while an earlier row of the rows that mounted with it still loads, so that a burst of
 * rows shows in order. A reader who scrolls up mounts the rows below the tall row before the tall row, so they show
 * before it mounts. The test must do the same, or the tall row holds them hidden:
 *
 * - The coarse step is shorter than the distance from the bottom edge of the second row below the tall row to the
 *   bottom edge of the tall row, about 240px. So no coarse step that mounts that second row mounts the tall row too.
 * - The fine step is shorter than the first row below the tall row, a rule of about 20px with its gap. So that row
 *   mounts one step before the tall row.
 */
const COARSE_STEP_PX = 150
const COARSE_STEPS_MAX = 30
const FINE_STEP_PX = 16
const FINE_STEPS_MAX = 40

/** How far below the top of the view the placeholder starts in a test that places it high in the view. */
const HIGH_SLOT_TOP_PX = 100

/** How far above the bottom of the view the placeholder starts in the test that places it low in the view. */
const LOW_SLOT_ABOVE_BOTTOM_PX = 120

/**
 * The tall rows by their height in views, with the number of paragraphs that gives that height at ROW_WIDTH. Each
 * test checks the height that the row takes.
 */
const ROW_SIZES = [
  { views: 3, paragraphs: 6 },
  { views: 5, paragraphs: 9 },
  { views: 10, paragraphs: 15 },
] as const

/** A short scripted turn. */
function shortTurn(turn: number) {
  return { prompt: `Turn ${turn}.`, answer: `Answer ${turn}.`, entry: 'insert' as const }
}

/** A seeded chat whose tall row waits for its render. */
interface LateChat {
  /** The sequence number of the tall row. */
  lateSeq: string
  /** The hold on the render of the tall row. */
  held: HeldRenderReplies
  /** The height of the view. */
  viewHeight: number
}

/**
 * Seed short turns, a tall row of `paragraphs` paragraphs, and more short turns on a wide screen. Then load the page
 * again on a narrow screen, with no stored render and with the render of the tall row held back, and make each row
 * ROW_WIDTH wide. The view is at the bottom of the chat.
 */
async function seedChatWithLateRow(page: Page, script: ModelScript, paragraphs: number): Promise<LateChat> {
  await page.setViewportSize({ width: SEED_WIDTH, height: VIEW_HEIGHT })
  await waitForAgentStarted(page)
  let turn = 0
  for (let i = 0; i < TURNS_ABOVE; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  const report = `${widthSensitiveParagraphs(LATE_LABEL, paragraphs)}\n\n${largeBodyFiller()}`
  await sendLongTurn(page, script, 'Reply with a long report.', report, `${LATE_LABEL}${paragraphs - 1}:`)
  const lateSeq = await readRowSeqWithText(page, `${LATE_LABEL}0:`)
  expect(lateSeq, 'the tall row is mounted at the tail').not.toBeNull()
  for (let i = 0; i < TURNS_BELOW; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  await expectAtBottom(page, 'after the seed')

  await page.setViewportSize({ width: NARROW_WIDTH, height: VIEW_HEIGHT })
  await expectAtBottom(page, 'on a narrow screen')
  // The chat takes the whole width in the layout of a narrow screen, less a fixed inset.
  const inset = NARROW_WIDTH - await readContentBoxWidth(page)

  const held = await holdRenderRepliesWith(page, `${LATE_LABEL}0:`)
  await clearStoredRenders(page)
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: `Answer ${turn}.` }), 'the last answer shows after the reload').toHaveCount(1)
  await expectAtBottom(page, 'after the reload')
  // The tall row cannot measure at this width: its render is held, and no height of this 8px step is stored.
  await page.setViewportSize({ width: ROW_WIDTH + inset, height: VIEW_HEIGHT })
  await expect.poll(() => readContentBoxWidth(page), { message: `each row is ${ROW_WIDTH}px wide` }).toBe(ROW_WIDTH)
  await expectAtBottom(page, `at a row width of ${ROW_WIDTH}px`)
  const viewHeight = await chatScrollContainer(page).evaluate(container => container.clientHeight)
  return { lateSeq: lateSeq!, held, viewHeight }
}

/** The rows that the app hides in a view of `viewHeight`, other than the tall row. */
function hiddenRowsInView(states: RowState[], lateSeq: string, viewHeight: number): RowState[] {
  return states.filter(row => row.seq !== lateSeq && row.hidden && row.distance < viewHeight && row.distance + row.height > 0)
}

/** Scroll up by `pixels`, then wait until every mounted row other than the tall row shows its content. */
async function stepUp(page: Page, chat: LateChat, pixels: number): Promise<void> {
  await dragChatUp(page, pixels)
  await expect.poll(async () => (await readRowStates(page)).filter(row => row.seq !== chat.lateSeq && row.hidden).length, {
    message: 'every mounted row other than the tall row shows its content',
  }).toBe(0)
}

/**
 * Scroll up as a reader does, so that each row below the tall row shows before the tall row mounts (see
 * COARSE_STEP_PX). The tall row mounts above the view. Then place its placeholder `slotTop` below the top of the
 * view.
 */
async function scrollUpToLateRow(page: Page, chat: LateChat, slotTop: number): Promise<void> {
  const secondRowBelow = String(Number(chat.lateSeq) + 2)
  expect(await readRowBox(page, chat.lateSeq), 'the tall row is not mounted at the bottom of the chat').toBeNull()
  for (let step = 0; await readRowBox(page, secondRowBelow) === null; step++) {
    expect(step, 'the rows below the tall row mount within the expected number of coarse steps').toBeLessThan(COARSE_STEPS_MAX)
    await stepUp(page, chat, COARSE_STEP_PX)
  }
  expect(await readRowBox(page, chat.lateSeq), 'the rows below the tall row mount before it').toBeNull()
  let box = await readRowBox(page, chat.lateSeq)
  for (let step = 0; box === null; step++) {
    expect(step, 'the tall row mounts within the expected number of fine steps').toBeLessThan(FINE_STEPS_MAX)
    await stepUp(page, chat, FINE_STEP_PX)
    box = await readRowBox(page, chat.lateSeq)
  }
  // The reader reaches a row that mounts above the view from below. The test needs that.
  expect(box.distance, 'the tall row mounts above the view').toBeLessThan(0)
  await stepUp(page, chat, slotTop - box.distance)
  await expect.poll(async () => (await readRowBox(page, chat.lateSeq))?.distance, {
    message: `the placeholder of the tall row starts ${slotTop}px below the top of the view`,
  }).toBeCloseTo(slotTop, 0)
}

/** The rows that show their content in a view of `viewHeight`, other than the tall row. */
function shownRowsInView(states: RowState[], lateSeq: string, viewHeight: number): RowState[] {
  return states.filter(row => row.seq !== lateSeq && !row.hidden && row.distance < viewHeight && row.distance + row.height > 0)
}

/**
 * Wait until the render of the tall row is held and every other row in the view shows its content. Then expect that
 * the tall row still waits behind a placeholder that is far shorter than the row will be. Returns the rows.
 */
async function expectLateRowWaits(page: Page, chat: LateChat): Promise<RowState[]> {
  await expect.poll(chat.held.heldCount, { message: 'the render of the tall row is held' }).toBeGreaterThan(0)
  await waitForScrollRest(page)
  const states = await readRowStates(page)
  expect(hiddenRowsInView(states, chat.lateSeq, chat.viewHeight), 'every other row in the view shows its content').toEqual([])
  const late = states.find(row => row.seq === chat.lateSeq)
  expect(late?.hidden, 'the tall row still waits for its content').toBe(true)
  const next = states.find(row => Number(row.seq) > Number(chat.lateSeq))
  expect(next, 'a row below the tall row is mounted').toBeDefined()
  expect(next!.distance - late!.distance, 'the placeholder is shorter than half a view').toBeLessThan(chat.viewHeight / 2)
  return states
}

/** Deliver the render of the tall row, wait until the row shows it, and return the rows at rest. */
async function deliverLateRow(page: Page, chat: LateChat): Promise<RowState[]> {
  await chat.held.release()
  await expect(page.locator(`[data-seq="${chat.lateSeq}"]:visible`), 'the tall row shows its content').toHaveCount(1)
  await expect.poll(async () => (await readRowStates(page)).find(row => row.seq === chat.lateSeq)?.hidden, {
    message: 'the app shows the tall row',
  }).toBe(false)
  await waitForScrollRest(page)
  return readRowStates(page)
}

/** Expect that each row of `before` is still mounted in `after`, at most 1px from its distance in `before`. */
function expectRowsHeld(before: RowState[], after: RowState[], which: string): void {
  expect(before.length, `${which}: at least one row shows in the view`).toBeGreaterThan(0)
  for (const row of before) {
    const now = after.find(candidate => candidate.seq === row.seq)
    expect(now, `${which}: the row of seq ${row.seq} is still mounted`).toBeDefined()
    expect(Math.abs(now!.distance - row.distance), `${which}: the row of seq ${row.seq} stays in place, within 1px (it moved from ${row.distance}px to ${now!.distance}px)`).toBeLessThanOrEqual(1)
  }
}

test.describe('chat scroll stability while the content of a tall row arrives late', () => {
  for (const size of ROW_SIZES) {
    test(`keeps the rows below a row of ${size.views} views in place when its content arrives high in the view`, async ({ page, authenticatedWorkspace, modelScript }) => {
      const chat = await seedChatWithLateRow(page, modelScript, size.paragraphs)
      await scrollUpToLateRow(page, chat, HIGH_SLOT_TOP_PX)
      const before = await expectLateRowWaits(page, chat)
      const below = shownRowsInView(before, chat.lateSeq, chat.viewHeight).filter(row => Number(row.seq) > Number(chat.lateSeq))
      const tailBefore = await chatDistanceFromBottom(page)

      const after = await deliverLateRow(page, chat)

      const late = after.find(row => row.seq === chat.lateSeq)!
      expect(late.height / chat.viewHeight, `the row is about ${size.views} views tall`).toBeGreaterThan(size.views - 0.5)
      expect(late.height / chat.viewHeight, `the row is about ${size.views} views tall`).toBeLessThan(size.views + 0.5)
      expectRowsHeld(below, after, 'below the tall row')
      const tailMoved = await chatDistanceFromBottom(page) - tailBefore
      expect(Math.abs(tailMoved), `the tail of the chat stays as far below the view, within 1px (it moved by ${tailMoved}px)`).toBeLessThanOrEqual(1)
      // The row grew above itself, so it now starts above the view.
      expect(late.distance, 'the tall row starts above the view').toBeLessThan(0)
    })
  }

  test('keeps the rows above a tall row in place when its content arrives low in the view', async ({ page, authenticatedWorkspace, modelScript }) => {
    const size = ROW_SIZES[0]
    const chat = await seedChatWithLateRow(page, modelScript, size.paragraphs)
    const slotTop = chat.viewHeight - LOW_SLOT_ABOVE_BOTTOM_PX
    await scrollUpToLateRow(page, chat, slotTop)
    const before = await expectLateRowWaits(page, chat)
    const above = shownRowsInView(before, chat.lateSeq, chat.viewHeight).filter(row => Number(row.seq) < Number(chat.lateSeq))

    const after = await deliverLateRow(page, chat)

    const late = after.find(row => row.seq === chat.lateSeq)!
    expect(late.height / chat.viewHeight, `the row is about ${size.views} views tall`).toBeGreaterThan(size.views - 0.5)
    expectRowsHeld(above, after, 'above the tall row')
    // The row grew at its bottom, so it starts where its placeholder started and ends below the view.
    expect(Math.abs(late.distance - slotTop), `the tall row starts where its placeholder started, within 1px (at ${late.distance}px)`).toBeLessThanOrEqual(1)
    expect(late.distance + late.height, 'the tall row ends below the view').toBeGreaterThan(chat.viewHeight)
  })
})
