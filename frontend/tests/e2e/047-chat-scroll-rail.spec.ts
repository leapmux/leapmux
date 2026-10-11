import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { PREVIEW, RAIL, RAIL_DOT, RAIL_FILLER_PREVIEW, seedOverflowingConversation, THUMB } from './helpers/chatScrollRail'
import { selectedText } from './helpers/selection'
import { chatScrollContainer, USER_BUBBLE_SELECTOR, waitForAgentStarted } from './helpers/ui'

/**
 * Smoke test for the seq-space chat scroll rail. The geometry math, the marks store,
 * the seek/jump wiring, and the paginator's fetch-around-seq are exhaustively unit
 * tested (chatScrollRailGeometry.test.ts, chatMessageMarks.test.ts, chat.store.test.ts,
 * chatHistoryPaginator.test.ts, useChatScroll.seek.test.ts, ChatScrollRail.test.tsx).
 * This covers the bits that need a real browser: the native scrollbar being hidden, a
 * teal dot rendering for each of the user's own messages, and clicking a dot jumping to
 * that message.
 */

/**
 * How far the page clock runs while the pointer rests on the preview card, before the check that
 * the card is still there. Comfortably longer than POINTER_CLOSE_DELAY_MS, so a card that ignored
 * the pointer has closed by then.
 *
 * The test runs the page's fake clock instead of waiting: `clock.runFor` fires every timer that
 * falls due inside the run, the close timer included, before it returns. So the check cannot run
 * before the close timer, which a wall-clock wait cannot promise under load.
 */
const POPOVER_LINGER_MS = 1000

/**
 * The virtual row wrapper (carries data-seq) for the Nth user message bubble.
 * Scoped to the scroll container, which excludes the hidden premeasure copies (they mount outside it) and the dots of
 * the rail (which reuse data-seq).
 */
function userRow(page: Page, nth: number) {
  return chatScrollContainer(page)
    .locator('[data-seq]')
    .filter({ has: page.locator(USER_BUBBLE_SELECTOR) })
    .nth(nth)
}

test.describe('chat scroll rail', () => {
  test('hides the native scrollbar, dots each user message, and jumps on a dot click', async ({ page, authenticatedWorkspace, modelScript }) => {
    // A short viewport so a couple of tall user bubbles overflow and the rail appears.
    await page.setViewportSize({ width: 720, height: 380 })
    // The fake clock flows with real time until a step below runs it ahead. The reload starts the
    // app under it, so every timer and every `performance.now()` of the app reads the one clock.
    // This spec is in ISOLATED_CONTEXT_SPECS, because no API removes the clock from its context.
    await page.clock.install()
    await page.reload()

    await waitForAgentStarted(page)

    // The native scrollbar is hidden on the chat container -- the rail replaces it. This
    // holds regardless of conversation length, so assert it up front.
    const scroller = chatScrollContainer(page)
    await expect(scroller).toBeVisible()
    // Polled: the rail decides whether it owns scrolling from a MEASURED
    // viewport height, so right after setViewportSize the native bar is still
    // 'thin' until the resize observation lands.
    await expect.poll(() => scroller.evaluate(el => getComputedStyle(el).scrollbarWidth)).toBe('none')

    // Send two tall messages. Both user messages land (their server echoes carry real
    // seqs), and they overflow the short viewport, so the rail shows.
    await seedOverflowingConversation(page, modelScript, 2)

    // The rail shows with a thumb.
    const rail = page.locator(RAIL)
    const thumb = page.locator(THUMB)
    await expect(thumb).toBeVisible()

    // Each user message has a teal jump dot at its seq (there may be additional dots for
    // any control responses, so assert per-user-message rather than an exact total).
    const firstUserSeq = await userRow(page, 0).getAttribute('data-seq')
    const secondUserSeq = await userRow(page, 1).getAttribute('data-seq')
    expect(firstUserSeq).not.toBeNull()
    expect(secondUserSeq).not.toBeNull()
    const dot = (seq: string | null) => page.locator(`${RAIL_DOT}[data-seq="${seq}"]`)
    await expect(dot(firstUserSeq)).toHaveCount(1)
    await expect(dot(secondUserSeq)).toHaveCount(1)

    // Hovering a dot previews that message's content in a popover (shown immediately). The
    // message text begins with a fixed phrase, so the preview (extracted + truncated on the
    // client) must contain it.
    await dot(firstUserSeq).hover()
    const preview = page.locator(PREVIEW)
    await expect(preview).toContainText(RAIL_FILLER_PREVIEW)

    // The card is a place the reader can GO: the pointer leaves the dot, crosses the gutter, and
    // lands on the card, which then stays for as long as the pointer rests on it. Only a real
    // browser covers this -- it needs the card's pointer-events (a media query jsdom never
    // evaluates) and a hit-test that reaches it. See POINTER_CLOSE_DELAY_MS.
    const previewBox = await preview.boundingBox()
    expect(previewBox).not.toBeNull()
    await page.mouse.move(previewBox!.x + previewBox!.width / 2, previewBox!.y + previewBox!.height / 2)
    await page.clock.runFor(POPOVER_LINGER_MS)
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
    const selected = await selectedText(page)
    expect(selected.length, 'the drag must leave a selection the reader can copy').toBeGreaterThan(0)
    await page.clock.runFor(POPOVER_LINGER_MS)
    await expect(preview).toBeVisible()

    // The reader's next click collapses that selection, and the card lets go with it.
    await page.mouse.click(previewBox!.x - 200, previewBox!.y)
    await expect(preview).toHaveCount(0)

    // Moving away closes it (after the same delay), so it is a card the reader visits, not a panel.
    await dot(firstUserSeq).hover()
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
    await dot(firstUserSeq).click()
    await expect(userRow(page, 0)).toBeInViewport()
  })
})
