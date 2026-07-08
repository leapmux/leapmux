import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { waitForAgentIdle } from './helpers/ui'

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

async function sendMessage(page: Page, message: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(message)
  await page.keyboard.press('Meta+Enter')
  await expect(editor).toHaveText('')
}

/** The virtual row wrapper (carries data-seq) for the Nth user message bubble. */
function userRow(page: Page, nth: number) {
  return page
    .locator('[data-seq]')
    .filter({ has: page.locator('[data-testid="message-bubble"][data-role="user"]') })
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
    expect(await scroller.evaluate(el => getComputedStyle(el).scrollbarWidth)).toBe('none')

    // Send two messages, waiting for each turn to finish.
    await sendMessage(page, LONG_MESSAGE)
    await waitForAgentIdle(page)
    await sendMessage(page, LONG_MESSAGE)
    await waitForAgentIdle(page)

    // Two user messages landed (their server echoes carry real seqs).
    const userBubbles = page.locator('[data-testid="message-bubble"][data-role="user"]')
    await expect(userBubbles).toHaveCount(2)

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
    await expect(page.locator('[data-testid="chat-scroll-rail-preview"]')).toContainText('Please just reply with "ok"')

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
