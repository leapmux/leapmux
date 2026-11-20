import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { ROW_ALIGN_OFFSET_PX } from '~/components/chat/chatLayoutShift'
import { MESSAGE_PAGE_LIMIT } from '../../src/generated/contracts/chat-history'
import { WorkerAgentService } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { procedureOf } from '../../src/lib/rpcProcedure'
import { test } from './fixtures'
import { RAIL, RAIL_DOT } from './helpers/chatScrollRail'
import { clearStoredChatHeightsAndRenders, expectNoVisibleHeightChange, expectRowMoved, expectStoredRowHeight, markedParagraphs, pointAtChat, readRowBox, readRowSeqWithText, recordPremeasureCommits, recordRowHeightChanges, requireTopRow, sendLongTurn, waitForScrollRest, watchFrames, watchRow, wheelChatUp } from './helpers/chatScrollStability'
import { sentRpcMethods } from './helpers/notificationCopy'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { installRpcListeners } from './helpers/timingFixture'
import { touchSwipe } from './helpers/touch'
import { assistantBubbles, chatScrollContainer, chatScrollTop, userBubbles, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps the content under the reader in place while older pages load above the reader. A wheel of N
 * pixels up moves every row down the screen by exactly N pixels, so a step that moves a row by any other distance is a
 * jump.
 *
 * No test here scrolls through rows that measure for the first time: the hidden premeasure gives a short history its
 * heights before the reader reaches it, so such a test passes with the layout shift frozen. The unit tests of the shift
 * rule cover that case.
 *
 * A seek to a row of an older page also changes the history above the reader. The seek waits only for the rows from
 * its row down, so its row joins the list first, and the rows above it join after the landing. The row lands at the
 * same distance below the top of the view as every other seek, and it stays there when the rows above it join.
 *
 * An older page whose rows have stored heights joins the list in one step, and every row in view keeps its element
 * while it does. A row that mounts again vanishes for a moment, and during a fling it comes back as a skeleton.
 */

/** The distance of one wheel step. */
const WHEEL_STEP_PX = 400

/** The most short turns that the seed sends before it fails: about three times the turns that one page holds. */
const MAX_SEED_TURNS = 40

/** The most wheel steps that a reader needs to reach a row of an older page. */
const MAX_WHEEL_STEPS = 40

/**
 * The long turns that open the history of the test of stored heights, and the paragraphs of each answer: a line or more
 * each. The older page that holds the answers is then taller than the band of rows that the app keeps mounted beyond
 * the view, which is 800px for the chat of a 600px viewport (computeOverscanPx). A page shorter than that band leaves
 * the rows in view mounted, whatever range the app computes for it.
 */
const LONG_TURNS = 2
const LONG_ANSWER_PARAGRAPHS = 40

/** A short scripted turn whose prompt and answer state its number. */
function shortTurn(turn: number) {
  return { prompt: `Turn ${turn}: reply with its number.`, answer: `Answer ${turn}.`, entry: 'insert' as const }
}

/** The highest sequence number of the mounted rows: at the bottom of the chat, the newest row on screen. */
function newestMountedSeq(page: Page): Promise<number> {
  return chatScrollContainer(page).evaluate(container =>
    Math.max(0, ...Array.from(container.querySelectorAll<HTMLElement>('[data-seq]')).map(row => Number(row.dataset.seq))))
}

/**
 * Send short turns, from turn `sent + 1` on, until the newest rows lie more than one page after the row of `seq`.
 * After the chat opens again it loads the newest page alone, so the row of `seq` then loads only when the reader asks
 * for it. Resolves the number of the last turn.
 */
async function sendTurnsUntilPastOnePage(page: Page, modelScript: ModelScript, seq: number, sent: number): Promise<number> {
  const result = await sendShortTurnsPastSeq(page, modelScript, seq + MESSAGE_PAGE_LIMIT + 1, sent, () => newestMountedSeq(page))
  return result.turn
}

async function sendShortTurnsPastSeq(
  page: Page,
  modelScript: ModelScript,
  target: number,
  sent: number,
  readSeq: (turn: number) => Promise<number>,
): Promise<{ turn: number, seq: number }> {
  let turns = sent
  let seq = await readSeq(turns)
  while (seq <= target) {
    turns++
    expect(turns, 'the history grows past one page within the expected number of turns').toBeLessThanOrEqual(MAX_SEED_TURNS)
    await sendScriptedTurn(page, modelScript, shortTurn(turns))
    seq = await readSeq(turns)
  }
  return { turn: turns, seq }
}

/**
 * Wheel the chat up until `target` is mounted. A wheel step below the top of the loaded rows moves every row down the
 * screen by the wheel distance. At the top of the loaded rows, a wheel up asks for the older page, and the row at the
 * top stays where it is in every frame while the page arrives above it.
 */
async function wheelUpUntilMounted(page: Page, target: Locator, what: string): Promise<void> {
  for (let step = 1; await target.count() === 0; step++) {
    expect(step, `the reader reaches ${what} within the expected number of steps`).toBeLessThanOrEqual(MAX_WHEEL_STEPS)
    const top = await chatScrollTop(page)
    const before = await requireTopRow(page)
    if (top >= 1) {
      // A step never passes the top of the loaded rows, so the browser applies the whole wheel distance. A position
      // below one pixel counts as the top, because a wheel of less than a pixel scrolls nothing.
      const pixels = Math.min(WHEEL_STEP_PX, top)
      await wheelChatUp(page, pixels)
      await expectRowMoved(page, before, pixels, `wheel step ${step}`)
      continue
    }
    const watch = await watchRow(page, before.seq)
    await pointAtChat(page)
    await page.mouse.wheel(0, -WHEEL_STEP_PX)
    await expect.poll(() => chatScrollTop(page), { message: 'an older page arrives above the reader' }).toBeGreaterThan(0)
    await waitForScrollRest(page)
    await watch.expectStill(`while the older page of step ${step} arrives`)
  }
}

test.describe('chat scroll stability while the history above the reader changes', () => {
  test('keeps the content in place while older pages load above the reader', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 600 })
    await waitForAgentStarted(page)
    await sendScriptedTurn(page, modelScript, shortTurn(1))
    const firstSeqText = await readRowSeqWithText(page, 'Turn 1:')
    expect(firstSeqText, 'the row of the first prompt is mounted').not.toBeNull()
    // After a reload the first prompt loads only when the reader scrolls up to it.
    const turns = await sendTurnsUntilPastOnePage(page, modelScript, Number(firstSeqText), 1)

    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: `Answer ${turns}.` })).toHaveCount(1)
    await waitForScrollRest(page)
    const firstPrompt = userBubbles(page).filter({ hasText: 'Turn 1:' })
    // Every frame of the scroll, and every height that the app commits during it: a row that changes its height in view
    // moves a part of the view against the rest, also where no step checks its distance.
    const heights = await recordRowHeightChanges(page)
    const frames = await watchFrames(page)

    // An older page loads ahead of the reader during a wheel step, or at the top of the loaded rows. A step that a load
    // moved fails its distance check, and a load at the top fails its watch.
    await wheelUpUntilMounted(page, firstPrompt, 'the first turn')
    await expect(firstPrompt).toBeVisible()
    await frames.expectNoJump('while the reader scrolls up through the history')
    await expectNoVisibleHeightChange(heights, 'after the reader scrolled up through the history')
  })

  test('lands a seek on a row of an older page at its place while the rows above it wait for their measure', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 600 })
    await waitForAgentStarted(page)
    await sendScriptedTurn(page, modelScript, shortTurn(1))
    await sendScriptedTurn(page, modelScript, shortTurn(2))
    // The target of the seek is the second prompt, so its page holds the rows of the first turn above it.
    const targetSeq = await readRowSeqWithText(page, 'Turn 2:')
    expect(targetSeq, 'the row of the second prompt is mounted').not.toBeNull()
    // Pick a newer prompt past the amount a 600px pane fetches for a seek.
    const distant = await sendShortTurnsPastSeq(page, modelScript, Number(targetSeq) + 16, 2, async (turn) => {
      const seq = await readRowSeqWithText(page, `Turn ${turn}:`)
      expect(seq, 'the candidate prompt is mounted').not.toBeNull()
      return Number(seq)
    })
    const skippedSeq = String(distant.seq)
    const turns = await sendTurnsUntilPastOnePage(page, modelScript, distant.seq, distant.turn)

    // Open the chat again as a browser does that never showed it. No row has a stored height then, so the rows of the
    // page of the seek measure for the first time.
    const chatUrl = page.url()
    await clearStoredChatHeightsAndRenders(page)
    await page.goto(chatUrl)
    await expect(assistantBubbles(page).filter({ hasText: `Answer ${turns}.` }), 'the last answer shows when the chat opens again').toHaveCount(1)
    await waitForScrollRest(page)
    await expect(userBubbles(page).filter({ hasText: 'Turn 2:' }), 'the second prompt is not loaded').toHaveCount(0)

    const mark = page.locator(`${RAIL_DOT}[data-seq="${targetSeq}"]`)
    const premeasure = await recordPremeasureCommits(page)
    // Hover first: the pointer on the strip keeps the rail lit for the click.
    await mark.hover()
    await expect(page.locator(RAIL)).toHaveCSS('opacity', '1')
    await mark.click()

    /** How far the row of the prompt is from its place, ROW_ALIGN_OFFSET_PX below the top of the view. */
    const distanceFromPlace = async (): Promise<number> => {
      const box = await readRowBox(page, targetSeq!)
      return box === null
        ? Number.POSITIVE_INFINITY
        : Math.abs(box.distance - ROW_ALIGN_OFFSET_PX)
    }
    // The seek waits only for the rows from the prompt down, so the prompt is the first row of the list when it lands.
    // The top of the list is then not the top of the loaded rows, and the prompt lands as a row in the middle does.
    await expect.poll(distanceFromPlace, { message: `the prompt lands ${ROW_ALIGN_OFFSET_PX}px below the top of the view` }).toBeLessThanOrEqual(1)
    // The rows of the first turn join the list above the prompt after the landing, and the prompt stays in its place.
    await expect(userBubbles(page).filter({ hasText: 'Turn 1:' }), 'the first turn joins the list above the prompt').toHaveCount(1)
    await waitForScrollRest(page)
    expect(await distanceFromPlace(), 'the prompt stays where it landed').toBeLessThanOrEqual(1)
    expect(await premeasure.commitOf(skippedSeq), 'the browser leaves the distant prompt for normal scrolling').toBeNull()

    await installRpcListeners(page)
    const distantMark = page.locator(`${RAIL_DOT}[data-seq="${skippedSeq}"]`)
    await distantMark.hover()
    await distantMark.click()
    await expect(userBubbles(page).filter({ hasText: `Turn ${distant.turn}:` })).toBeVisible()
    expect(await sentRpcMethods(page)).toContain(procedureOf(WorkerAgentService.method.listAgentMessages))

    await page.setViewportSize({ width: 1024, height: 5000 })
    const chat = chatScrollContainer(page)
    await expect.poll(() => chat.evaluate(element => element.scrollHeight - element.clientHeight)).toBeLessThanOrEqual(4)
    await installRpcListeners(page)
    const box = await chat.boundingBox()
    expect(box).not.toBeNull()
    await touchSwipe(page, {
      from: { x: box!.x + box!.width / 2, y: box!.y + 220 },
      to: { x: box!.x + box!.width / 2, y: box!.y + 100 },
    })
    await expect.poll(() => sentRpcMethods(page)).toContain(procedureOf(WorkerAgentService.method.listAgentMessages))
  })

  test('keeps every row in view mounted while an older page with stored heights arrives', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 600 })
    await waitForAgentStarted(page)
    // The long turns open the history, and the short turns push them out of the newest page.
    for (let turn = 1; turn <= LONG_TURNS; turn++) {
      const label = `Long${turn}P`
      await sendLongTurn(page, modelScript, `Long ${turn}: write the report.`, markedParagraphs(label, LONG_ANSWER_PARAGRAPHS, 'every word of this paragraph makes the report a little taller.'), `${label}${LONG_ANSWER_PARAGRAPHS - 1}:`)
    }
    const lastLongSeq = await readRowSeqWithText(page, `Long${LONG_TURNS}P0:`)
    expect(lastLongSeq, 'the row of the last long answer is mounted').not.toBeNull()
    const lastLongId = await chatScrollContainer(page).locator(`[data-seq="${lastLongSeq}"]`).getAttribute('data-chat-row-id')
    expect(lastLongId).not.toBeNull()
    const turns = await sendTurnsUntilPastOnePage(page, modelScript, Number(lastLongSeq), 0)
    await expectStoredRowHeight(page, lastLongId!)

    // The reload keeps the row heights that the app stored. The chat opens with the newest page alone, so the page of the
    // long answers arrives later with the stored heights of its rows, and all of its rows join the list in one step.
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: `Answer ${turns}.` }), 'the last answer shows after the reload').toHaveCount(1)
    await waitForScrollRest(page)
    const lastLongAnswer = assistantBubbles(page).filter({ hasText: `Long${LONG_TURNS}P0:` })
    await expect(lastLongAnswer, 'the page of the long answers is not loaded').toHaveCount(0)
    const heights = await recordRowHeightChanges(page)
    // The app loads the stored heights of an arriving page in an effect, and a later effect of that batch renders the
    // rows at the viewport. That effect must read the layout shift of the new rows: with the shift of the old ones, it
    // renders rows a page above the view, and every row in view unmounts and mounts again. The frames show such a row
    // as a new element.
    const frames = await watchFrames(page)

    await wheelUpUntilMounted(page, lastLongAnswer, 'the last long answer')

    expect(await heights.all()).toContainEqual(expect.objectContaining({
      seq: lastLongSeq,
      source: 'storage',
    }))
    await frames.expectNoJump('while the page of the long answers arrives above the reader')
  })
})
