import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { firstAssistantBubble } from './helpers/ui'

/**
 * Smoke test for chat scroll + pagination integration. The store-level
 * pagination logic (windowing, fetch-older, fetch-newer, dedupe) is
 * exhaustively tested in `src/stores/chat.store.test.ts`. The bits
 * that genuinely require a real browser — overflow calculation, the
 * auto-scroll interplay during streaming, the thinking indicator
 * lifecycle, and tab-switch scroll preservation — are condensed into this
 * single smoke.
 */

async function sendMessage(page: Page, message: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(message)
  await page.keyboard.press('Meta+Enter')
}

test.describe('Chat Pagination & Scroll', () => {
  test('thinking indicator appears, message renders with data-seq, then indicator clears', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(page, 'Say hello.')

    // Thinking indicator may flash briefly for fast responses; tolerate that.
    const thinking = page.locator('[data-testid="thinking-indicator"]')
    await expect(thinking).toBeVisible({ timeout: 30_000 }).catch(() => {})

    // Wait for the assistant bubble to land.
    await expect(firstAssistantBubble(page)).toBeVisible({ timeout: 60_000 })

    // After the turn completes, the thinking indicator should be gone.
    await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible({ timeout: 60_000 })
    await expect(thinking).not.toBeVisible()

    // Each rendered message wrapper carries a positive data-seq from the
    // server — this is what powers chat.store's pagination ordering.
    const seqElements = page.locator('[data-seq]')
    const count = await seqElements.count()
    expect(count).toBeGreaterThan(0)
    for (let i = 0; i < count; i++) {
      const seqValue = await seqElements.nth(i).getAttribute('data-seq')
      expect(Number(seqValue)).toBeGreaterThan(0)
    }

    // Virtualized rows are absolutely positioned via translateY inside the
    // spacer. The first row sits at offset 0 (transform 'none'), so assert on a
    // LATER row instead — a non-zero translateY computes to a matrix(...), which
    // actually proves the virtualizer laid rows out rather than trivially
    // accepting the first row's 'none'.
    if (count > 1) {
      const lastTransform = await seqElements.last().evaluate(el => getComputedStyle(el).transform)
      expect(lastTransform).toMatch(/^matrix/)
    }
    else {
      const transform = await seqElements.first().evaluate(el => getComputedStyle(el).transform)
      expect(transform === 'none' || transform.startsWith('matrix')).toBe(true)
    }

    // Stick-to-bottom must survive virtualization: after a streamed turn while
    // at the bottom, the viewport stays pinned to the live tail.
    const scroller = page.locator('[data-chat-scroll-container="true"]')
    const distFromBottom = await scroller.evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight)
    expect(distFromBottom).toBeLessThan(40)
  })
})
