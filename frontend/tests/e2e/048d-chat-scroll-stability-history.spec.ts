import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { MESSAGE_PAGE_LIMIT } from '../../src/generated/contracts/chat-history'
import { test } from './fixtures'
import { expectRowMoved, pointAtChat, readRowSeqWithText, requireTopRow, waitForScrollRest, watchRow, wheelChatUp } from './helpers/chatScrollStability'
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

test.describe('chat scroll stability while the history above the reader changes', () => {
  test('keeps the content in place while older pages load above the reader', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 600 })
    await waitForAgentStarted(page)
    await sendScriptedTurn(page, modelScript, shortTurn(1))
    const firstSeqText = await readRowSeqWithText(page, 'Turn 1:')
    expect(firstSeqText, 'the row of the first prompt is mounted').not.toBeNull()
    const firstSeq = Number(firstSeqText)
    // Send turns until the newest rows lie more than one page after the first prompt. After a reload the chat loads
    // the newest page alone, so the first prompt loads only when the reader scrolls up to it.
    let turns = 1
    while (await newestMountedSeq(page) - firstSeq <= MESSAGE_PAGE_LIMIT + 1) {
      turns++
      expect(turns, 'the history grows past one page within the expected number of turns').toBeLessThanOrEqual(MAX_SEED_TURNS)
      await sendScriptedTurn(page, modelScript, shortTurn(turns))
    }

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
})
