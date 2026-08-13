import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { sendMessage, USER_BUBBLE_SELECTOR, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * Smoke test for the seq-space chat scroll rail. The geometry math, the marks store,
 * the seek/jump wiring, and the paginator's fetch-around-seq are exhaustively unit
 * tested (chatScrollRailGeometry.test.ts, chatMessageMarks.test.ts, chat.store.test.ts,
 * chatHistoryPaginator.test.ts, useChatScroll.seek.test.ts, ChatScrollRail.test.tsx).
 * This covers the bits that need a real browser: the native scrollbar being hidden, a
 * teal dot rendering for each of the user's own messages, and clicking a dot jumping to
 * that message.
 */

// A deliberately long message so each user bubble is tall enough that two of them
// overflow the short viewport below -- otherwise the rail correctly hides itself.
const LONG_MESSAGE = `Please just reply with "ok". Ignore this filler: ${'the quick brown fox jumps over the lazy dog. '.repeat(12)}`

/**
 * How long to rest on the preview card before checking that it is still there. Comfortably
 * longer than POINTER_CLOSE_DELAY_MS, so a card that ignored the pointer would already be gone.
 * A fixed wait is the only way to assert that something does NOT happen within a window.
 */
const POPOVER_LINGER_MS = 1000

/** The virtual row wrapper (carries data-seq) for the Nth user message bubble. */
function userRow(page: Page, nth: number) {
  return page
    .locator('[data-seq]')
    .filter({ has: page.locator(USER_BUBBLE_SELECTOR) })
    .nth(nth)
}

test.describe('chat scroll rail', () => {
  test('hides the native scrollbar, dots each user message, and jumps on a dot click', async ({ page, authenticatedWorkspace }) => {
    // A short viewport so a couple of tall user bubbles overflow and the rail appears.
    await page.setViewportSize({ width: 720, height: 380 })

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    // Let the agent finish starting so the send takes the fast path (see 010).
    await expect(page.getByText(/^Starting /)).not.toBeVisible()

    // The native scrollbar is hidden on the chat container -- the rail replaces it. This
    // holds regardless of conversation length, so assert it up front.
    const scroller = page.locator('[data-chat-scroll-container="true"]')
    await expect(scroller).toBeVisible()
    // Polled: the rail decides whether it owns scrolling from a MEASURED
    // viewport height, so right after setViewportSize the native bar is still
    // 'thin' until the resize observation lands.
    await expect.poll(() => scroller.evaluate(el => getComputedStyle(el).scrollbarWidth)).toBe('none')

    // Send two messages, waiting for each turn to finish.
    await sendMessage(page, LONG_MESSAGE)
    await waitForAgentIdle(page)
    await sendMessage(page, LONG_MESSAGE)
    await waitForAgentIdle(page)

    // Two user messages landed (their server echoes carry real seqs).
    await expect(userBubbles(page)).toHaveCount(2)

    // The tall bubbles overflow the short viewport, so the rail shows with a thumb.
    const rail = page.locator('[data-testid="chat-scroll-rail"]')
    await expect(rail).toBeVisible()
    const thumb = page.locator('[data-testid="chat-scroll-rail-thumb"]')
    await expect(thumb).toBeVisible()

    // Each user message has a teal jump dot at its seq (there may be additional dots for
    // any control responses, so assert per-user-message rather than an exact total).
    const firstUserSeq = await userRow(page, 0).getAttribute('data-seq')
    const secondUserSeq = await userRow(page, 1).getAttribute('data-seq')
    expect(firstUserSeq).not.toBeNull()
    expect(secondUserSeq).not.toBeNull()
    await expect(page.locator(`[data-testid="chat-scroll-rail-dot"][data-seq="${firstUserSeq}"]`)).toHaveCount(1)
    await expect(page.locator(`[data-testid="chat-scroll-rail-dot"][data-seq="${secondUserSeq}"]`)).toHaveCount(1)

    // Hovering a dot previews that message's content in a popover (shown immediately). The
    // message text begins with a fixed phrase, so the preview (extracted + truncated on the
    // client) must contain it.
    await page.locator(`[data-testid="chat-scroll-rail-dot"][data-seq="${firstUserSeq}"]`).hover()
    const preview = page.locator('[data-testid="chat-scroll-rail-preview"]')
    await expect(preview).toContainText('Please just reply with "ok"')

    // The card is a place the reader can GO: the pointer leaves the dot, crosses the gutter, and
    // lands on the card, which then stays for as long as the pointer rests on it. Only a real
    // browser covers this -- it needs the card's pointer-events (a media query jsdom never
    // evaluates) and a hit-test that reaches it. See POINTER_CLOSE_DELAY_MS.
    const previewBox = await preview.boundingBox()
    expect(previewBox).not.toBeNull()
    await page.mouse.move(previewBox!.x + previewBox!.width / 2, previewBox!.y + previewBox!.height / 2)
    await page.waitForTimeout(POPOVER_LINGER_MS)
    await expect(preview).toBeVisible()
    // And its text is selectable, although the rail around it sets user-select: none so a thumb
    // drag never selects anything.
    await expect(preview).toHaveCSS('user-select', 'text')

    // A real drag-select inside the card, ending OUTSIDE it -- what selecting to the end of a line
    // does, because the card's right edge is only a gutter away from the rail. The card must
    // outlive that release, and the selection must survive with it. Only a real browser has a
    // selection engine, so nothing but this run covers it. The drag starts inside the first line
    // of text (past the card's own inset) and ends just past the right edge, which is still on
    // screen -- a release beyond the viewport would extend no selection at all.
    await page.mouse.move(previewBox!.x + 12, previewBox!.y + 14)
    await page.mouse.down()
    await page.mouse.move(previewBox!.x + previewBox!.width + 4, previewBox!.y + 14, { steps: 10 })
    await page.mouse.up()
    const selected = await page.evaluate(() => document.getSelection()?.toString() ?? '')
    expect(selected.length, 'the drag must leave a selection the reader can copy').toBeGreaterThan(0)
    await page.waitForTimeout(POPOVER_LINGER_MS)
    await expect(preview).toBeVisible()

    // The reader's next click collapses that selection, and the card lets go with it.
    await page.mouse.click(previewBox!.x - 200, previewBox!.y)
    await expect(preview).toHaveCount(0)

    // Moving away closes it (after the same delay), so it is a card the reader visits, not a panel.
    await page.locator(`[data-testid="chat-scroll-rail-dot"][data-seq="${firstUserSeq}"]`).hover()
    await expect(preview).toBeVisible()
    await page.mouse.move(previewBox!.x - 200, previewBox!.y)
    await expect(preview).toHaveCount(0)

    // The thumb is sized to the viewport's share of the conversation, not the whole rail.
    const railBox = await rail.boundingBox()
    const thumbBox = await thumb.boundingBox()
    expect(railBox).not.toBeNull()
    expect(thumbBox).not.toBeNull()
    expect(thumbBox!.height).toBeLessThan(railBox!.height)

    // Clicking the FIRST user message's dot jumps the view (scrolled to the tail) up to it.
    await page.locator(`[data-testid="chat-scroll-rail-dot"][data-seq="${firstUserSeq}"]`).click()
    await expect(userRow(page, 0)).toBeInViewport()
  })
})
