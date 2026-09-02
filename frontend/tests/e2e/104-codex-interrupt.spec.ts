import { codexTest, expect } from './codex-fixtures'
import { messageBubbles, sendMessage } from './helpers/ui'

codexTest.describe('Codex Interrupt', () => {
  codexTest('send a prompt and interrupt mid-response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Long prompt to ensure the interrupt window is wide enough that the
    // Interrupt button is observable. If the button never appears, the
    // assertion below must fail — do not gate it on isMaybeVisible.
    await sendMessage(page, 'Write a very detailed essay about the history of computing, at least 5000 words across multiple chapters with subheadings.')

    // Click Interrupt — required to appear within the timeout.
    const interruptBtn = page.locator('[data-testid="interrupt-button"]')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()

    // After interrupt, the thinking indicator must clear, and at least
    // one user + partial-response bubble must be present.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
    const bubbles = messageBubbles(page)
    expect(await bubbles.count()).toBeGreaterThan(1)
  })
})
