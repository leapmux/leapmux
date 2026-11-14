import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { assistantBubbles, chatScrollContainer, sendMessage, waitForAgentIdle, waitForAgentStarted } from './helpers/ui'

/**
 * The chat view keeps what the reader sees in place while the content changes. The layout rules are unit tested
 * (`chatLayoutShift.test.ts`, `useChatVirtualizer.geometry.test.ts`, `useChatScroll.*.test.ts`). Only a real browser
 * rewraps a row when the pane width changes, and only a real browser delivers a wheel scroll and the row growth of a
 * live answer in its own order.
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

/** The paragraph whose top edge is the first one inside the scroll container. */
interface TopParagraph {
  marker: string
  /** The distance from the top edge of the scroll container to the top edge of the paragraph. */
  distance: number
  /** The height of the row that holds the paragraph. */
  rowHeight: number
}

/**
 * Read the paragraph at the top of the chat, its distance from the container top, and the height of its row, in one
 * browser operation. The hidden premeasure copies mount outside the scroll container, so the read sees only the
 * visible rows.
 */
async function readTopParagraph(page: Page): Promise<TopParagraph | null> {
  return chatScrollContainer(page).evaluate((container, markerSource) => {
    const marker = new RegExp(markerSource)
    const top = container.getBoundingClientRect().top
    for (const paragraph of container.querySelectorAll('p')) {
      const text = paragraph.textContent ?? ''
      const match = marker.exec(text)
      const distance = paragraph.getBoundingClientRect().top - top
      if (match === null || distance < 0)
        continue
      const row = paragraph.closest('[data-seq]')
      if (row === null)
        return null
      return { marker: match[0], distance, rowHeight: row.getBoundingClientRect().height }
    }
    return null
  }, PARAGRAPH_MARKER.source)
}

/** Wait until the chat stops scrolling: two reads of `scrollTop`, one frame apart, are equal. */
async function waitForScrollRest(page: Page): Promise<void> {
  await expect.poll(() => chatScrollContainer(page).evaluate(container => new Promise<boolean>((resolve) => {
    const before = container.scrollTop
    requestAnimationFrame(() => resolve(container.scrollTop === before))
  })), { message: 'the chat stops scrolling' }).toBe(true)
}

/** The distance from the bottom of the view to the bottom of the content. */
async function distanceFromBottom(page: Page): Promise<number> {
  return chatScrollContainer(page).evaluate(container => container.scrollHeight - container.scrollTop - container.clientHeight)
}

/** Put the pointer over the middle of the chat, where a wheel scrolls it. */
async function pointAtChat(page: Page): Promise<void> {
  const box = await chatScrollContainer(page).boundingBox()
  expect(box, 'the chat has a box on screen').not.toBeNull()
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
}

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
    const atBottom = await readTopParagraph(page)
    expect(atBottom, 'a paragraph of the answer is on screen').not.toBeNull()
    await pointAtChat(page)
    await page.mouse.wheel(0, -Math.round(atBottom!.rowHeight / 2))
    await waitForScrollRest(page)

    const before = await readTopParagraph(page)
    expect(before, 'a paragraph of the answer is at the top').not.toBeNull()
    expect(before!.marker, 'the view is inside the answer, not at its first paragraph').not.toBe('P0:')

    await page.setViewportSize({ width: 1100, height: 700 })
    await expect.poll(async () => (await readTopParagraph(page))?.rowHeight, {
      message: 'the answer rewraps to the new width',
    }).not.toBe(before!.rowHeight)
    await waitForScrollRest(page)

    const after = await readTopParagraph(page)
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
    await waitForScrollRest(page)

    await pointAtChat(page)
    await page.mouse.wheel(0, -30)
    await waitForScrollRest(page)
    expect(await distanceFromBottom(page), 'the wheel moved the view above the bottom').toBeGreaterThan(2)
    const scrollTop = await chatScrollContainer(page).evaluate(container => container.scrollTop)

    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(step + 1)
    await expect(assistantBubbles(page).filter({ hasText: 'Held answer line 12.' })).toHaveCount(1)
    await waitForAgentIdle(page)
    await waitForScrollRest(page)

    expect(Math.abs(await chatScrollContainer(page).evaluate(container => container.scrollTop) - scrollTop)).toBeLessThanOrEqual(1)
    await expect(page.getByRole('button', { name: 'Scroll to bottom' })).toBeVisible()
  })
})
