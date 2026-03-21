import { codexTest, expect } from './codex-fixtures'
import { sendMessage } from './helpers/ui'

codexTest.describe('Codex Interrupt', () => {
  codexTest('send a prompt and interrupt mid-response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Send a long prompt to give time to interrupt.
    await sendMessage(page, 'Write a very detailed essay about the history of computing, at least 2000 words.')

    // Wait briefly for the agent to start responding.
    await page.waitForTimeout(3000)

    // Click the interrupt/stop button.
    const stopBtn = page.locator('[data-testid="interrupt-btn"], [data-testid="stop-btn"]')
    if (await stopBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
      await stopBtn.click()

      // Wait for the turn to complete (interrupted).
      await page.waitForTimeout(5000)

      // Verify that some content was received before the interrupt.
      const bubbles = page.locator('[data-testid="message-bubble"]')
      const count = await bubbles.count()
      expect(count).toBeGreaterThan(1) // at least user message + partial response
    }
  })

  codexTest('interrupted turn shows completion status', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Write a 5000-word analysis of quantum computing.')

    await page.waitForTimeout(3000)

    const stopBtn = page.locator('[data-testid="interrupt-btn"], [data-testid="stop-btn"]')
    if (await stopBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
      await stopBtn.click()
      await page.waitForTimeout(5000)

      // After interrupt, the thinking indicator should be gone.
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).not.toBeVisible({ timeout: 10_000 })
    }
  })
})
