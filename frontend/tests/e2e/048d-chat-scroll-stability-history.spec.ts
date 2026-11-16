import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { ROW_ALIGN_OFFSET_PX } from '~/components/chat/chatLayoutShift'
import { MESSAGE_PAGE_LIMIT } from '../../src/generated/contracts/chat-history'
import { test } from './fixtures'
import { RAIL, RAIL_DOT } from './helpers/chatScrollRail'
import { clearStoredRenders, clearStoredRowHeights, expectRowMoved, pointAtChat, readRowBox, readRowSeqWithText, requireTopRow, waitForScrollRest, watchRow, wheelChatUp } from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
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
 */

/** The distance of one wheel step. */
const WHEEL_STEP_PX = 400

/** The most short turns that the seed sends before it fails: about three times the turns that one page holds. */
const MAX_SEED_TURNS = 40

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
  let turns = sent
  while (await newestMountedSeq(page) - seq <= MESSAGE_PAGE_LIMIT + 1) {
    turns++
    expect(turns, 'the history grows past one page within the expected number of turns').toBeLessThanOrEqual(MAX_SEED_TURNS)
    await sendScriptedTurn(page, modelScript, shortTurn(turns))
  }
  return turns
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

    for (let step = 1; await firstPrompt.count() === 0; step++) {
      expect(step, 'the reader reaches the first turn within the expected number of steps').toBeLessThanOrEqual(40)
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
      // At the top of the loaded rows, a wheel up asks for the older page. It arrives above the reader, and the rows
      // under the reader stay where they are in every frame.
      const watch = await watchRow(page, before.seq)
      await pointAtChat(page)
      await page.mouse.wheel(0, -WHEEL_STEP_PX)
      await expect.poll(() => chatScrollTop(page), { message: 'an older page arrives above the reader' }).toBeGreaterThan(0)
      await waitForScrollRest(page)
      await watch.expectStill(`while the older page of step ${step} arrives`)
    }
    // An older page loads ahead of the reader during a wheel step, or at the top of the loaded rows. A step that a load
    // moved fails its distance check, and a load at the top fails its watch.
    await expect(firstPrompt).toBeVisible()
  })

  test('lands a seek on a row of an older page at its place while the rows above it wait for their measure', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 600 })
    await waitForAgentStarted(page)
    await sendScriptedTurn(page, modelScript, shortTurn(1))
    await sendScriptedTurn(page, modelScript, shortTurn(2))
    // The target of the seek is the second prompt, so its page holds the rows of the first turn above it.
    const targetSeq = await readRowSeqWithText(page, 'Turn 2:')
    expect(targetSeq, 'the row of the second prompt is mounted').not.toBeNull()
    const turns = await sendTurnsUntilPastOnePage(page, modelScript, Number(targetSeq), 2)

    // Open the chat again as a browser does that never showed it. No row has a stored height then, so the rows of the
    // page of the seek measure for the first time.
    const chatUrl = page.url()
    await clearStoredRowHeights(page)
    // The helper above left the browser on an empty page of the app's origin.
    await clearStoredRenders(page)
    await page.goto(chatUrl)
    await expect(assistantBubbles(page).filter({ hasText: `Answer ${turns}.` }), 'the last answer shows when the chat opens again').toHaveCount(1)
    await waitForScrollRest(page)
    await expect(userBubbles(page).filter({ hasText: 'Turn 2:' }), 'the second prompt is not loaded').toHaveCount(0)

    const mark = page.locator(`${RAIL_DOT}[data-seq="${targetSeq}"]`)
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
  })
})
