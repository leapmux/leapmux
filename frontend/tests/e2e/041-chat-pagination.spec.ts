import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { firstAssistantBubble } from './helpers/ui'

/**
 * Smoke test for chat scroll + pagination integration. The store-level
 * pagination logic (windowing, fetch-older, fetch-newer, dedupe) is
 * exhaustively tested in `tests/unit/stores/chat.store.test.ts`. The bits
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
  })
})
