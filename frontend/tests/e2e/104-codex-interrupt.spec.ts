import { codexTest, expect } from './codex-fixtures'
import { sendMessage } from './helpers/ui'

codexTest.describe('Codex Interrupt', () => {
  codexTest('send a prompt and interrupt mid-response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Long prompt to ensure the interrupt window is wide enough that the
    // stop button is observable. If the stop button never appears, the
    // assertion below must fail — do not gate it on isMaybeVisible.
    await sendMessage(page, 'Write a very detailed essay about the history of computing, at least 5000 words across multiple chapters with subheadings.')

    // Click the interrupt/stop button — required to appear within the timeout.
    const stopBtn = page.locator('[data-testid="interrupt-btn"], [data-testid="stop-btn"]')
    await expect(stopBtn).toBeVisible({ timeout: 30_000 })
    await stopBtn.click()

    // After interrupt, the thinking indicator must clear, and at least
    // one user + partial-response bubble must be present.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible({ timeout: 30_000 })
    const bubbles = page.locator('[data-testid="message-bubble"]')
    expect(await bubbles.count()).toBeGreaterThan(1)
  })

  codexTest('interrupted turn shows completion status', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Write a 5000-word analysis of quantum computing with detailed chapters and citations.')

    const stopBtn = page.locator('[data-testid="interrupt-btn"], [data-testid="stop-btn"]')
    await expect(stopBtn).toBeVisible({ timeout: 30_000 })
    await stopBtn.click()

    // After interrupt, the thinking indicator must be gone.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible({ timeout: 30_000 })
  })
})
