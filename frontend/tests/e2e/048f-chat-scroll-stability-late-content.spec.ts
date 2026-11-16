import type { Page } from '@playwright/test'
import type { HeldRenderReplies, RowState } from './helpers/chatScrollStability'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { ROW_ALIGN_OFFSET_PX } from '~/components/chat/chatLayoutShift'
import { test } from './fixtures'
import { RAIL, RAIL_DOT } from './helpers/chatScrollRail'
import {
  clearStoredRenders,
  clearStoredRowHeights,
  dragChatUp,
  expectAtBottom,
  expectRowHeld,
  expectTailFartherBy,
  framesPass,
  holdRenderRepliesWith,
  largeBodyFiller,
  loadingIndicator,
  readMountedRowSeqs,
  readRowBox,
  readRowSeqWithText,
  readRowStates,
  readViewportHeight,
  recordPremeasureCommits,
  requireTopRow,
  resizeRowsAtBottom,
  resizeRowsTo,
  sendLongTurn,
  shortTurn,
  waitForScrollRest,
  watchRow,
  widthSensitiveParagraphs,
} from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { assistantBubbles, chatDistanceFromBottom, chatScrollContainer, chatScrollTop, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view shows a row only after the app measured it, so the content of a tall row that arrives late moves
 * nothing that the reader looks at.
 *
 * A worker renders the Markdown of a row, and a hidden copy of the row measures it once the render arrived. Until
 * then the row is not in the list: the app does not know its height, and it guesses none. A long report takes the
 * longest to render, and a slow device takes longer still. So a reader can reach the end of the measured rows:
 *
 * - A reader who scrolls up stops at the last measured row, and the app shows "Loading older messages..." there. When
 *   the row above is measured, it joins the list above the view, and the rows in the view stay where they are.
 * - A chat whose last row is not measured shows "Loading messages..." in place of its first view. It shows the
 *   complete view once, when that row is measured.
 * - A seek to a row waits until the rows that the view shows below that row are measured, and it lands then.
 * - A width change makes the app measure again every row that is not on screen. The view keeps its place, and the
 *   reader reaches the rows above once the app measured them again.
 *
 * Each test seeds the chat, then loads it again as a browser does that never rendered its rows, and holds back the
 * render of the tall row.
 *
 * The source of the tall row ends with a large link definition that draws nothing (see largeBodyFiller). The app then
 * renders the row in a worker of its own and waits about 40 seconds for it, where it waits 10 seconds for a row of
 * ordinary size (see holdRenderRepliesWith). So the hold outlasts the scroll of the test, also under load.
 *
 * No test here reaches the branches of the layout-shift rule (chatLayoutShift.ts) for a row whose first read in the
 * list differs from the height of its hidden copy. Such a row needs a request to the Worker (the read of an image
 * file, or the lookup of the other half of a tool call) that outlasts READINESS_REQUEST_LIMIT_MS (renderReadiness.ts)
 * and ends before the row mounts. The hold above delays a reply of a render worker in the browser, and no seam of this
 * suite can delay one request to the Worker, so chatLayoutShift.test.ts and useChatVirtualizer.geometry.test.ts cover
 * those branches.
 */

const VIEW_HEIGHT = 600

/** The width of the viewport while the test seeds the chat: the width of the other scroll stability specs. */
const SEED_WIDTH = 1024

/**
 * The first narrow width of the viewport, at which the test loads the page again. Its rows are wider than ROW_WIDTH,
 * so no row has a stored height at ROW_WIDTH.
 */
const NARROW_WIDTH = 440

/** The width of the rows while the reader scrolls. */
const ROW_WIDTH = 372

/** The change of the row width in the test of a width change: a few pixels, as a scrollbar or a side panel takes. */
const WIDTH_CHANGE_PX = 5

/** The short turns above the tall row. */
const TURNS_ABOVE = 2

/**
 * The short turns below the tall row: about 2300px of rows on a narrow screen. At the bottom of the chat, the tall
 * row then lies about 1800px above the view, more than the 800px band of rows that the app keeps mounted there.
 */
const TURNS_BELOW = 8

/** The label of the paragraphs of the tall row (see widthSensitiveParagraphs). */
const LATE_LABEL = 'Late'

/** One scroll step of the reader, and the most steps that a walk to the end of the measured rows needs. */
const STEP_PX = 300
const STEPS_MAX = 200

/** A step that changes the distance of the tail by less than this moved the view no farther. */
const MOVED_PX = 1

/** The distance of each scroll in the test of a width change: it ends in the short rows below the tall row. */
const SCROLL_UP_PX = 1500

/** The animation frames over which the test of the first view watches a row. */
const WATCH_FRAMES = 30

/** What the app shows while the reader waits at the top of the list for rows that it still measures. */
const LOADING_OLDER = 'Loading older messages...'

/** What the app shows in place of a first view whose rows it still measures. */
const LOADING_FIRST_VIEW = 'Loading messages...'

/**
 * The tall rows by their height in views, with the number of paragraphs that gives that height at ROW_WIDTH. Each
 * test checks the height that the row takes.
 */
const ROW_SIZES = [
  { views: 3, paragraphs: 6 },
  { views: 5, paragraphs: 9 },
  { views: 10, paragraphs: 15 },
] as const

/** A seeded chat whose tall row waits for its render. */
interface LateChat {
  /** The sequence number of the tall row. */
  lateSeq: string
  /** The sequence number of the row below the tall row in the list. */
  rowAfterLateSeq: number
  /** The hold on the render of the tall row. */
  held: HeldRenderReplies
  /** The height of the view. */
  viewHeight: number
  /**
   * How far above the bottom edge of the view the tall row ended on the narrow screen, with the view at the bottom of
   * the chat and before the page loaded again. Null when the row was not mounted there, because short turns lie
   * below it.
   */
  lateEndAboveBottom: number | null
}

/** How `seedChatWithLateRow` seeds the chat and loads it again. */
interface SeedOptions {
  /** The short turns below the tall row. With 0, the tall row is the last message of the chat. */
  turnsBelow?: number
  /**
   * Open the chat again with no stored row height, in place of a reload. A chat that ends with the tall row then has
   * no measured row at its end, so its first view waits.
   */
  firstViewWaits?: boolean
}

/**
 * Seed short turns, a tall row of `paragraphs` paragraphs, and `turnsBelow` more short turns on a wide screen. Then
 * load the page again on a narrow screen, with no stored render and with the render of the tall row held back, and
 * make each row ROW_WIDTH wide.
 *
 * The view is at the bottom of the chat. With `firstViewWaits`, no row has a stored height either, and the caller
 * gets a chat that shows no answer.
 */
async function seedChatWithLateRow(
  page: Page,
  script: ModelScript,
  paragraphs: number,
  { turnsBelow = TURNS_BELOW, firstViewWaits = false }: SeedOptions = {},
): Promise<LateChat> {
  await page.setViewportSize({ width: SEED_WIDTH, height: VIEW_HEIGHT })
  await waitForAgentStarted(page)
  let turn = 0
  for (let i = 0; i < TURNS_ABOVE; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  const report = `${widthSensitiveParagraphs(LATE_LABEL, paragraphs)}\n\n${largeBodyFiller()}`
  await sendLongTurn(page, script, 'Reply with a long report.', report, `${LATE_LABEL}${paragraphs - 1}:`)
  const lateSeq = await readRowSeqWithText(page, `${LATE_LABEL}0:`)
  expect(lateSeq, 'the tall row is mounted at the tail').not.toBeNull()
  // Read the row below the tall row while the view is at the tail. After the short turns below, that row can be too
  // far above the view to be mounted.
  const rowAfterLateSeq = await readRowSeqAfter(page, Number(lateSeq))
  for (let i = 0; i < turnsBelow; i++)
    await sendScriptedTurn(page, script, shortTurn(++turn))
  await expectAtBottom(page, 'after the seed')

  await page.setViewportSize({ width: NARROW_WIDTH, height: VIEW_HEIGHT })
  await expectAtBottom(page, 'on a narrow screen')
  const lateEndAboveBottom = await readRowEndAboveBottom(page, lateSeq!)

  const held = await holdRenderRepliesWith(page, `${LATE_LABEL}0:`)
  if (firstViewWaits) {
    const chatUrl = page.url()
    await clearStoredRowHeights(page)
    // The helper above left the browser on an empty page of the app's origin.
    await clearStoredRenders(page)
    await page.goto(chatUrl)
    await expect(chatScrollContainer(page), 'the chat opens again').toBeVisible()
    await resizeRowsTo(page, ROW_WIDTH)
    await expect(assistantBubbles(page), 'no answer shows while the first view waits').toHaveCount(0)
  }
  else {
    await clearStoredRenders(page)
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: `Answer ${turn}.` }), 'the last answer shows after the reload').toHaveCount(1)
    await expectAtBottom(page, 'after the reload')
    // The tall row cannot measure at this width: its render is held, and no height of this width is stored.
    await resizeRowsAtBottom(page, ROW_WIDTH)
  }
  const viewHeight = await readViewportHeight(page)
  return {
    lateSeq: lateSeq!,
    rowAfterLateSeq,
    held,
    viewHeight,
    lateEndAboveBottom,
  }
}

/**
 * Read how far above the bottom edge of the view the row of `seq` ends, or null when the row is not mounted. The
 * value is negative for a row that ends below the view.
 */
async function readRowEndAboveBottom(page: Page, seq: string): Promise<number | null> {
  const box = await readRowBox(page, seq)
  if (box === null)
    return null
  const viewHeight = await readViewportHeight(page)
  return viewHeight - (box.distance + box.height)
}

/**
 * Read the sequence number of the first row below the row of `seq` in the list, once such a row is mounted. It is not
 * always `seq + 1`: a message that the chat does not show, such as a hidden frame of the provider, takes a sequence
 * number and draws no row.
 */
async function readRowSeqAfter(page: Page, seq: number): Promise<number> {
  let after = Number.POSITIVE_INFINITY
  await expect.poll(async () => {
    after = Math.min(...(await readMountedRowSeqs(page)).filter(mounted => mounted > seq))
    return after
  }, { message: `a row below the row of seq ${seq} is in the list` }).toBeLessThan(Number.POSITIVE_INFINITY)
  return after
}

/**
 * Scroll up in steps of STEP_PX to the end of the measured rows: a step moves the view no farther, and the first row
 * of the list is the row after the tall row. The tall row cannot join the list, because its render is held.
 *
 * The app measures the rows below the tall row again after the width change of the seed. So a step can also stop at
 * a row farther down, while the app still measures the rows above that row. They join the list above the view then,
 * and the view can move up again.
 */
async function scrollUpToMeasuredEnd(page: Page, chat: LateChat): Promise<void> {
  for (let step = 0; ; step++) {
    expect(step, 'the reader reaches the end of the measured rows within the expected number of steps').toBeLessThan(STEPS_MAX)
    const before = await chatDistanceFromBottom(page)
    await dragChatUp(page, STEP_PX)
    if (await chatDistanceFromBottom(page) - before >= MOVED_PX)
      continue
    if ((await readMountedRowSeqs(page))[0] === chat.rowAfterLateSeq)
      return
    await expect.poll(() => chatScrollTop(page), { message: 'the rows above join the list, so the view can move up again' }).toBeGreaterThan(0)
  }
}

/** The rows of `states` that lie in a view of `viewHeight`, in part or in whole. */
function rowsInView(states: RowState[], viewHeight: number): RowState[] {
  return states.filter(row => row.distance < viewHeight && row.distance + row.height > 0)
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

/**
 * The sequence number of the prompt of the tall row: of the rail marks, the last one before the tall row. The rail
 * draws one mark for each message of the user, also for a row that is not mounted.
 *
 * The app loads the marks in a request of their own, which can end after the rows show, and the rail draws no mark
 * until then. So the read waits for the marks.
 */
async function promptSeqOfLateRow(page: Page, chat: LateChat): Promise<string> {
  let before: number[] = []
  await expect.poll(async () => {
    const seqs = await page.locator(RAIL_DOT).evaluateAll(marks => marks.map(mark => Number((mark as HTMLElement).dataset.seq)))
    before = seqs.filter(seq => Number.isFinite(seq) && seq < Number(chat.lateSeq))
    return before.length
  }, { message: 'the rail has a mark for the prompt of the tall row' }).toBeGreaterThan(0)
  return String(Math.max(...before))
}

test.describe('chat scroll stability while the content of a tall row arrives late', () => {
  for (const size of ROW_SIZES) {
    test(`stops at a row of ${size.views} views whose content did not arrive, and keeps the view in place when it arrives`, async ({ page, authenticatedWorkspace, modelScript }) => {
      const chat = await seedChatWithLateRow(page, modelScript, size.paragraphs)
      await scrollUpToMeasuredEnd(page, chat)

      expect(await readRowBox(page, chat.lateSeq), 'the tall row is not in the list').toBeNull()
      await expect(loadingIndicator(page, LOADING_OLDER), 'the app says that it loads the rows above').toBeVisible()
      await expect.poll(chat.held.heldCount, { message: 'the render of the tall row is still held' }).toBeGreaterThan(0)
      await waitForScrollRest(page)
      const before = rowsInView(await readRowStates(page), chat.viewHeight)
      const tailBefore = await chatDistanceFromBottom(page)

      await chat.held.release()
      await expect(loadingIndicator(page, LOADING_OLDER), 'the indicator leaves once the tall row is measured').toHaveCount(0)
      await expect.poll(async () => await readRowBox(page, chat.lateSeq) !== null, { message: 'the tall row joins the list above the view' }).toBe(true)
      await waitForScrollRest(page)

      expectRowsHeld(before, await readRowStates(page), 'in the view')
      await expectTailFartherBy(page, tailBefore, 0, 'after the tall row joined the list')

      await dragChatUp(page, STEP_PX)
      const late = await readRowBox(page, chat.lateSeq)
      expect(late, 'the tall row is mounted').not.toBeNull()
      expect(late!.distance, 'the tall row starts above the view').toBeLessThan(0)
      const lateBottom = late!.distance + late!.height
      expect(lateBottom, 'the bottom part of the tall row is in the view').toBeGreaterThan(0)
      expect(lateBottom, 'the bottom part of the tall row is in the view').toBeLessThan(chat.viewHeight)
      await expectTailFartherBy(page, tailBefore, STEP_PX, 'after one more step')
      expect(late!.height / chat.viewHeight, `the row is about ${size.views} views tall`).toBeGreaterThan(size.views - 0.5)
      expect(late!.height / chat.viewHeight, `the row is about ${size.views} views tall`).toBeLessThan(size.views + 0.5)
    })
  }

  test('shows the first view once, after its last row is measured', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedChatWithLateRow(page, modelScript, ROW_SIZES[0].paragraphs, { turnsBelow: 0, firstViewWaits: true })

    await expect.poll(chat.held.heldCount, { message: 'the render of the tall row is held' }).toBeGreaterThan(0)
    await expect(loadingIndicator(page, LOADING_FIRST_VIEW), 'the app says that it loads the messages').toBeVisible()
    expect(await readMountedRowSeqs(page), 'no row is in the list while the first view waits').toEqual([])

    await chat.held.release()
    await expect.poll(async () => (await readMountedRowSeqs(page)).length, { message: 'the rows of the first view join the list' }).toBeGreaterThan(0)
    const top = await requireTopRow(page)
    const watch = await watchRow(page, top.seq)
    await expect(loadingIndicator(page, LOADING_FIRST_VIEW), 'the indicator leaves with the first view').toHaveCount(0)
    await expectAtBottom(page, 'in the first view')
    // The view shows the end of the chat as the reader left it: the tall row, and below it only the end of its turn.
    expect(chat.lateEndAboveBottom, 'the tall row was in the view at the bottom of the chat').not.toBeNull()
    const endAboveBottom = await readRowEndAboveBottom(page, chat.lateSeq)
    expect(endAboveBottom, 'the tall row is mounted').not.toBeNull()
    expect(
      Math.abs(endAboveBottom! - chat.lateEndAboveBottom!),
      `the tall row ends as far above the bottom of the view as before the chat opened again, within 1px (${endAboveBottom}px for ${chat.lateEndAboveBottom}px)`,
    ).toBeLessThanOrEqual(1)
    await framesPass(page, WATCH_FRAMES)
    await watch.expectStill('after the first view shows')
  })

  test('lands a seek on a row after the rows below it are measured', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedChatWithLateRow(page, modelScript, ROW_SIZES[0].paragraphs)
    const promptSeq = await promptSeqOfLateRow(page, chat)
    const mark = page.locator(`${RAIL_DOT}[data-seq="${promptSeq}"]`)

    // Hover first: the pointer on the strip keeps the rail lit for the click.
    await mark.hover()
    await expect(page.locator(RAIL)).toHaveCSS('opacity', '1')
    await mark.click()

    // The view of the seek holds the prompt and the top of the tall row, which is not measured.
    await expect.poll(chat.held.heldCount, { message: 'the render of the tall row is held' }).toBeGreaterThan(0)
    await expectAtBottom(page, 'while the seek waits for the tall row')

    await chat.held.release()
    await expect.poll(async () => {
      const box = await readRowBox(page, promptSeq)
      return box === null
        ? Number.POSITIVE_INFINITY
        : Math.abs(box.distance - ROW_ALIGN_OFFSET_PX)
    }, { message: `the prompt lands ${ROW_ALIGN_OFFSET_PX}px below the top of the view` }).toBeLessThanOrEqual(1)
  })

  test('reaches the rows above again after the width changes', async ({ page, authenticatedWorkspace, modelScript }) => {
    const chat = await seedChatWithLateRow(page, modelScript, ROW_SIZES[0].paragraphs)
    const premeasured = await recordPremeasureCommits(page)
    await chat.held.release()
    await expect.poll(() => premeasured.commitOf(chat.lateSeq), { message: 'the hidden copy measures the tall row' }).not.toBeNull()

    const tailAtBottom = await chatDistanceFromBottom(page)
    await dragChatUp(page, SCROLL_UP_PX)
    await expectTailFartherBy(page, tailAtBottom, SCROLL_UP_PX, 'after the first scroll')
    const top = await requireTopRow(page)
    const tailBefore = await chatDistanceFromBottom(page)

    await resizeRowsTo(page, ROW_WIDTH + WIDTH_CHANGE_PX)
    // The app measures again each row that is not on screen, and such a row is out of the list until then. So the
    // list ends above the tail for some frames, and the tail is as far below the view as before once the rows below
    // the view joined the list again. Each of them is one short line, which is as tall at the new width.
    await expect.poll(async () => Math.abs(await chatDistanceFromBottom(page) - tailBefore), {
      message: 'the rows below the view join the list again, and the tail is as far below the view as before, within 1px',
    }).toBeLessThanOrEqual(1)
    await waitForScrollRest(page)
    await expectRowHeld(page, top, 'after the width change')

    // A step up stops at the end of the measured rows, and the view can move up again once the rows above joined
    // the list.
    for (let step = 0; ; step++) {
      expect(step, 'the reader scrolls up within the expected number of steps').toBeLessThan(STEPS_MAX)
      const tailNow = await chatDistanceFromBottom(page)
      const remaining = SCROLL_UP_PX - (tailNow - tailBefore)
      if (remaining < MOVED_PX)
        break
      await dragChatUp(page, Math.min(STEP_PX, remaining))
      if (await chatDistanceFromBottom(page) - tailNow >= MOVED_PX)
        continue
      await expect(loadingIndicator(page, LOADING_OLDER), 'the rows above join the list').toHaveCount(0)
      await expect.poll(() => chatScrollTop(page), { message: 'the view can move up again' }).toBeGreaterThan(0)
    }
    await expectTailFartherBy(page, tailBefore, SCROLL_UP_PX, 'after the second scroll')
  })
})
