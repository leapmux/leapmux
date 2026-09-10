import { expect, test } from './fixtures'
import { firstAssistantBubble, readAttached, sendMessage } from './helpers/ui'

/**
 * Check chat layout and scroll position after a real streamed response.
 * `src/stores/chat.store.test.ts` covers pagination and duplicate removal.
 * This browser test checks row transforms, sequence attributes, and the final scroll position.
 */

test.describe('Chat Pagination & Scroll', () => {
  test('renders sequenced messages and clears the indicator after a response', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(page, 'Say hello.')

    const thinking = page.locator('[data-testid="thinking-indicator"]')

    // Wait for the assistant bubble to appear.
    await expect(firstAssistantBubble(page)).toBeVisible()

    // After the turn completes, the thinking indicator should be gone.
    await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible()
    await expect(thinking).not.toBeVisible()

    // Each rendered message wrapper carries a positive data-seq from the
    // server — this is what powers chat.store's pagination ordering.
    const seqElements = page.locator('[data-seq]')
    const count = await seqElements.count()
    expect(count).toBeGreaterThan(1)
    for (let i = 0; i < count; i++) {
      const seqValue = await seqElements.nth(i).getAttribute('data-seq')
      expect(Number(seqValue)).toBeGreaterThan(0)
    }

    // The first row has offset zero. A later row must have a nonzero transform.
    // Skip detached rows because replacing an entry creates its row again.
    // A detached row reports no transform. See readAttached.
    const rowTransform = (row: typeof seqElements) =>
      readAttached(row, 'the row transform', (matches) => {
        const el = matches.find(candidate => candidate.isConnected)
        return el ? getComputedStyle(el).transform : null
      })
    expect(await rowTransform(seqElements.last())).toMatch(/^matrix/)

    // The viewport must stay at the bottom after a streamed turn with virtualized rows.
    const scroller = page.locator('[data-chat-scroll-container="true"]')
    const distFromBottom = await scroller.evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight)
    expect(distFromBottom).toBeLessThan(40)
  })
})
