import { expect } from '@playwright/test'
import { test } from './fixtures'
import { readRowDistance, readTopParagraph, readTopRow, waitForScrollRest, waitForThinkingIndicator, wheelChatUp } from './helpers/chatScrollStability'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { assistantBubbles, chatDistanceFromBottom, chatScrollContainer, sendMessage, waitForAgentIdle, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps what the reader sees in place while the content changes. The layout rules are unit tested
 * (`chatLayoutShift.test.ts`, `useChatVirtualizer.geometry.test.ts`, `useChatScroll.*.test.ts`). Only a real browser
 * rewraps a row when the pane width changes, and only a real browser delivers a wheel scroll and the row growth of a
 * live answer in its own order. The other `048*` specs cover the output of the agent below a reader, a change of the
 * layout, and the older pages above a reader.
 */

/** The number of paragraphs of the long answer. */
const PARAGRAPH_COUNT = 40

/** About 350 characters, so that each paragraph wraps to several lines at both widths of the test. */
const PARAGRAPH_FILLER = 'The view must hold this paragraph in place while the pane changes its width. '.repeat(4).trim()

/** One marker per paragraph, such as `P17:`, so that a read identifies the paragraph at the top. */
const PARAGRAPH_MARKER = /^P\d+:/

/** A long prompt: two of its turns make the transcript taller than the short viewport of the second test. */
const LONG_PROMPT = `Please just reply with "ok". Ignore this filler: ${'the quick brown fox jumps over the lazy dog. '.repeat(12)}`

/** The answer of a held turn, one line per paragraph, so that its arrival grows the content below the reader. */
const HELD_ANSWER = Array.from({ length: 12 }, (_, i) => `Held answer line ${i + 1}.`).join('\n\n')

test.describe('chat scroll stability', () => {
  test('keeps the paragraph at the top in place when the pane width changes', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1280, height: 700 })
    await waitForAgentStarted(page)
    const answer = Array.from({ length: PARAGRAPH_COUNT }, (_, i) => `P${i}: ${PARAGRAPH_FILLER}`).join('\n\n')
    const step = await modelScript.queue({ text: answer })
    await sendMessage(page, modelScript.prompt('Reply with forty paragraphs.'))
    await modelScript.waitForSteps(step + 1)
    await expect(assistantBubbles(page).filter({ hasText: `P${PARAGRAPH_COUNT - 1}:` })).toHaveCount(1)
    await waitForAgentIdle(page)
    await waitForScrollRest(page)

    // The turn ends at the bottom of the answer. Half of its height up is the middle of the answer.
    const atBottom = await readTopParagraph(page, PARAGRAPH_MARKER)
    expect(atBottom, 'a paragraph of the answer is on screen').not.toBeNull()
    await wheelChatUp(page, Math.round(atBottom!.rowHeight / 2))

    const before = await readTopParagraph(page, PARAGRAPH_MARKER)
    expect(before, 'a paragraph of the answer is at the top').not.toBeNull()
    expect(before!.marker, 'the view is inside the answer, not at its first paragraph').not.toBe('P0:')

    await page.setViewportSize({ width: 1100, height: 700 })
    await expect.poll(async () => (await readTopParagraph(page, PARAGRAPH_MARKER))?.rowHeight, {
      message: 'the answer rewraps to the new width',
    }).not.toBe(before!.rowHeight)
    await waitForScrollRest(page)

    const after = await readTopParagraph(page, PARAGRAPH_MARKER)
    expect(after?.marker).toBe(before!.marker)
    expect(Math.abs(after!.distance - before!.distance), `${before!.marker} moves by less than 2px`).toBeLessThanOrEqual(2)
  })

  test('keeps a reader who scrolled up while the agent works', async ({ page, authenticatedWorkspace, modelScript }) => {
    await page.setViewportSize({ width: 1024, height: 480 })
    await waitForAgentStarted(page)
    await sendScriptedTurn(page, modelScript, { prompt: LONG_PROMPT, answer: 'ok' })
    await sendScriptedTurn(page, modelScript, { prompt: LONG_PROMPT, answer: 'ok' })
    expect(await chatScrollContainer(page).evaluate(container => container.scrollHeight - container.clientHeight), 'the transcript overflows the view')
      .toBeGreaterThan(100)

    const gate = 'chat-scroll-stability-answer'
    const step = await modelScript.queue({ text: HELD_ANSWER, gate })
    await sendMessage(page, modelScript.prompt('Reply with twelve lines.'))
    await modelScript.waitForGate(gate)
    await waitForThinkingIndicator(page)

    await wheelChatUp(page, 30)
    await expect.poll(() => chatDistanceFromBottom(page), { message: 'the wheel moved the view above the bottom' }).toBeGreaterThan(2)
    // The on-screen place of a row, not `scrollTop`: a rebase at rest changes `scrollTop` and moves nothing on screen.
    const before = await readTopRow(page)
    expect(before, 'a row is at the top of the view').not.toBeNull()

    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(step + 1)
    await expect(assistantBubbles(page).filter({ hasText: 'Held answer line 12.' })).toHaveCount(1)
    await waitForAgentIdle(page)
    await waitForScrollRest(page)

    const after = await readRowDistance(page, before!.seq)
    expect(after, `the row of seq ${before!.seq} stays mounted`).not.toBeNull()
    expect(Math.abs(after! - before!.distance), `the row of seq ${before!.seq} moves by at most 1px`).toBeLessThanOrEqual(1)
    await expect(page.getByRole('button', { name: 'Scroll to bottom' })).toBeVisible()
  })
})
