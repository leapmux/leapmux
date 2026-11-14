import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import {
  expectAtBottom,
  expectRowHeld,
  markedParagraphs,
  paragraphMarker,
  readChatWidth,
  readScrollHeight,
  readTopParagraph,
  readViewportHeight,
  requireTopRow,
  sendLongTurn,
  waitForScrollRest,
  waitForThinkingIndicator,
  watchRow,
  wheelChatUp,
} from './helpers/chatScrollStability'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './helpers/turnEndSound'
import { agentTabs, chatScrollContainer, focusComposer, openTerminalViaUI, PLATFORM_MOD, sendMessage, waitForAgentIdle, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps what the reader sees in place when the layout around the content changes: the height of the
 * window, the height of the composer, the width while the chat follows the tail or shows the top of the history, a
 * chat hidden behind another tab, and a device pixel ratio that is not a whole number.
 */

const VIEWPORT = { width: 1280, height: 700 }

/** Two narrower window widths. Each one narrows the chat, so the long answer rewraps. */
const NARROWER_WIDTH = 1100
const NARROWEST_WIDTH = 960

/** About 350 characters, so that each paragraph wraps to several lines at every width of the test. */
const FILLER = 'The view must hold this paragraph in place while the layout around it changes. '.repeat(4).trim()

/** The label of the paragraphs of the long answer, and the pattern of their markers. */
const LABEL = 'P'
const MARKER = paragraphMarker(LABEL)

/** The long answer: about ten views tall. */
const LONG_ANSWER = markedParagraphs(LABEL, 40, FILLER)

/** The gate at which the mock model holds an answer. */
const ANSWER_GATE = 'chat-scroll-stability-layout-answer'

/** Open the view at the size of the test, and send the turn of the long answer. The turn ends at the bottom. */
async function seedLongAnswer(page: Page, script: ModelScript): Promise<void> {
  await page.setViewportSize(VIEWPORT)
  await waitForAgentStarted(page)
  await sendLongTurn(page, script, 'Reply with forty paragraphs.', LONG_ANSWER, `${LABEL}39:`)
}

/** Scroll from the bottom of the long answer up into its middle. */
async function scrollIntoMiddle(page: Page): Promise<void> {
  const atBottom = await readTopParagraph(page, MARKER)
  expect(atBottom, 'a paragraph of the answer is on screen').not.toBeNull()
  await wheelChatUp(page, Math.round(atBottom!.rowHeight / 2))
}

/**
 * Change the window to `width`, and wait until the chat changes its width and the long answer rewraps: the height of
 * its row differs from `rowHeightBefore`. The first wait states the precondition, because the shell can give the chat
 * the same width at two window widths.
 */
async function resizeWidth(page: Page, width: number, rowHeightBefore: number): Promise<void> {
  const chatWidthBefore = await readChatWidth(page)
  await page.setViewportSize({ width, height: VIEWPORT.height })
  await expect.poll(() => readChatWidth(page), { message: `the chat changes its width at a window width of ${width}` }).not.toBe(chatWidthBefore)
  await waitForRewrap(page, rowHeightBefore)
}

/** Wait until the long answer rewraps after a change of the width: the height of its row differs from `before`. */
async function waitForRewrap(page: Page, rowHeightBefore: number): Promise<void> {
  await expect.poll(async () => (await readTopParagraph(page, MARKER))?.rowHeight, {
    message: 'the answer rewraps to the new width',
  }).not.toBe(rowHeightBefore)
  await waitForScrollRest(page)
}

/** Expect the paragraph at the top to keep its marker and its distance from the top, within 2px across a rewrap. */
async function expectParagraphHeld(page: Page, before: { marker: string, distance: number }, when: string): Promise<void> {
  const after = await readTopParagraph(page, MARKER)
  expect(after?.marker, `${when}: the same paragraph is at the top`).toBe(before.marker)
  expect(Math.abs(after!.distance - before.distance), `${when}: ${before.marker} moves by at most 2px`).toBeLessThanOrEqual(2)
}

/** Show the agent's chat again after another tab hid it. */
async function showAgentTab(page: Page): Promise<void> {
  await agentTabs(page).filter({ visible: true }).first().click()
  await expect(chatScrollContainer(page)).toBeVisible()
  await waitForScrollRest(page)
}

test.describe('chat scroll stability when the layout changes', () => {
  test('keeps the row at the top in place when the window height changes', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    await scrollIntoMiddle(page)
    const before = await requireTopRow(page)
    const viewportBefore = await readViewportHeight(page)
    const watch = await watchRow(page, before.seq)

    await page.setViewportSize({ width: VIEWPORT.width, height: 460 })
    await expect.poll(() => readViewportHeight(page), { message: 'the chat shrinks with the window' }).toBeLessThan(viewportBefore)
    await waitForScrollRest(page)
    await page.setViewportSize({ width: VIEWPORT.width, height: 900 })
    await expect.poll(() => readViewportHeight(page), { message: 'the chat grows with the window' }).toBeGreaterThan(viewportBefore)
    await waitForScrollRest(page)

    await watch.expectStill('while the window height changes')
    await expectRowHeld(page, before, 'after the window height changes')
  })

  test('keeps the row at the top in place while the composer grows and shrinks', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    await scrollIntoMiddle(page)
    const before = await requireTopRow(page)
    const viewportBefore = await readViewportHeight(page)
    const watch = await watchRow(page, before.seq)

    await focusComposer(page)
    for (let line = 1; line <= 6; line++) {
      await page.keyboard.insertText(`Draft line ${line}`)
      await page.keyboard.press('Enter')
    }
    await expect.poll(() => readViewportHeight(page), { message: 'the taller composer shrinks the chat' }).toBeLessThan(viewportBefore)
    await waitForScrollRest(page)
    await page.keyboard.press(`${PLATFORM_MOD}+a`)
    await page.keyboard.press('Backspace')
    await expect.poll(() => readViewportHeight(page), { message: 'the empty composer gives the chat its height back' }).toBe(viewportBefore)
    await waitForScrollRest(page)

    await watch.expectStill('while the composer grows and shrinks')
    await expectRowHeld(page, before, 'after the composer grows and shrinks')
  })

  test('keeps a follower at the bottom when the width changes', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    await expectAtBottom(page, 'after the turn')
    const atBottom = await readTopParagraph(page, MARKER)
    expect(atBottom, 'a paragraph of the answer is on screen').not.toBeNull()

    await resizeWidth(page, NARROWER_WIDTH, atBottom!.rowHeight)
    await expectAtBottom(page, 'after the chat narrows')
    const narrower = await readTopParagraph(page, MARKER)
    await resizeWidth(page, NARROWEST_WIDTH, narrower!.rowHeight)
    await expectAtBottom(page, 'after the chat narrows again')
    await expect(page.getByRole('button', { name: 'Scroll to bottom' })).toBeHidden()
  })

  test('keeps the top of the history in view when the width changes', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    await chatScrollContainer(page).evaluate((container) => {
      container.scrollTop = 0
    })
    await waitForScrollRest(page)
    const before = await requireTopRow(page)
    const paragraph = await readTopParagraph(page, MARKER)
    expect(paragraph, 'the first paragraphs of the answer are on screen').not.toBeNull()

    await resizeWidth(page, NARROWER_WIDTH, paragraph!.rowHeight)

    await expectRowHeld(page, before, 'after the chat narrows')
    expect(await chatScrollContainer(page).evaluate(container => container.scrollTop), 'the view stays at the top of the scroller').toBe(0)
  })

  test('keeps a reader in place when an answer arrives while another tab hides the chat', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    const step = await modelScript.queue({
      text: markedParagraphs('H', 30, FILLER),
      gate: ANSWER_GATE,
    })
    await sendMessage(page, modelScript.prompt('Reply with thirty paragraphs.'))
    await modelScript.waitForGate(ANSWER_GATE)
    await waitForThinkingIndicator(page)
    await wheelChatUp(page, VIEWPORT.height * 1.5)
    const before = await requireTopRow(page)
    const heightBefore = await readScrollHeight(page)

    await openTerminalViaUI(page)
    await expect(chatScrollContainer(page)).toBeHidden()
    const settled = await observeSettledReceipts(page)
    await modelScript.releaseGate(ANSWER_GATE)
    await modelScript.waitForSteps(step + 1)
    // The browser applies the end of the turn after the answer, while the chat is still hidden.
    await waitForIdleSoundReceipt(page, { agentId: authenticatedWorkspace.agentId, after: settled })
    await showAgentTab(page)
    await waitForScrollRest(page)

    await expectRowHeld(page, before, 'after the chat shows again')
    // A hidden row cannot measure, so the answer grows the content to its full height only once the chat shows.
    await expect.poll(() => readScrollHeight(page), { message: 'the answer is below the reader' }).toBeGreaterThan(heightBefore + 1000)
    await waitForScrollRest(page)
    await expectRowHeld(page, before, 'after the answer measures')
    await expect(page.getByRole('button', { name: 'Scroll to bottom' })).toBeVisible()
  })

  test('keeps the paragraph at the top in place when the width changes while another tab hides the chat', async ({ page, authenticatedWorkspace, modelScript }) => {
    await seedLongAnswer(page, modelScript)
    await scrollIntoMiddle(page)
    const before = await readTopParagraph(page, MARKER)
    expect(before, 'a paragraph of the answer is at the top').not.toBeNull()

    const chatWidthBefore = await readChatWidth(page)
    await openTerminalViaUI(page)
    await expect(chatScrollContainer(page)).toBeHidden()
    await page.setViewportSize({ width: NARROWER_WIDTH, height: VIEWPORT.height })
    await expect.poll(() => readChatWidth(page), { message: 'the hidden chat changes its width' }).not.toBe(chatWidthBefore)
    await showAgentTab(page)
    await waitForRewrap(page, before!.rowHeight)

    await expectParagraphHeld(page, before!, 'after the chat shows again at the new width')
  })

  test.describe('at a device pixel ratio of 1.5', () => {
    // A CSS pixel is 1.5 device pixels, so the browser rounds each written scroll position to a fraction of a pixel.
    test.use({ deviceScaleFactor: 1.5 })

    test('keeps the paragraph at the top in place when the width changes', async ({ page, authenticatedWorkspace, modelScript }) => {
      await seedLongAnswer(page, modelScript)
      await scrollIntoMiddle(page)
      const before = await readTopParagraph(page, MARKER)
      expect(before, 'a paragraph of the answer is at the top').not.toBeNull()

      await resizeWidth(page, NARROWER_WIDTH, before!.rowHeight)

      await expectParagraphHeld(page, before!, 'after the chat narrows')
    })

    test('keeps a reader in place when an answer arrives below', async ({ page, authenticatedWorkspace, modelScript }) => {
      await seedLongAnswer(page, modelScript)
      const step = await modelScript.queue({
        text: markedParagraphs('H', 30, FILLER),
        gate: ANSWER_GATE,
      })
      await sendMessage(page, modelScript.prompt('Reply with thirty paragraphs.'))
      await modelScript.waitForGate(ANSWER_GATE)
      await waitForThinkingIndicator(page)
      await wheelChatUp(page, VIEWPORT.height * 1.5)
      const before = await requireTopRow(page)
      const heightBefore = await readScrollHeight(page)
      const watch = await watchRow(page, before.seq)

      await modelScript.releaseGate(ANSWER_GATE)
      await modelScript.waitForSteps(step + 1)
      await expect.poll(() => readScrollHeight(page), { message: 'the answer arrives below the reader' }).toBeGreaterThan(heightBefore + 1000)
      await waitForAgentIdle(page)
      await waitForScrollRest(page)

      await watch.expectStill('while the answer arrives')
      await expectRowHeld(page, before, 'after the answer arrives')
    })
  })
})
