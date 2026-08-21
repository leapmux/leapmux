import { codexTest, expect } from './codex-fixtures'
import { messageBubbles, sendMessage } from './helpers/ui'

/**
 * Both tests are `fixme` on a known product defect, NOT on a flake:
 * https://github.com/leapmux/leapmux/issues/401 -- the thinking indicator
 * stays visible for the full timeout after the press on Interrupt.
 *
 * They never ran before. Each looked for `[data-testid="interrupt-btn"]` or
 * `[data-testid="stop-btn"]`, and the composer's button is `interrupt-button`,
 * so both always failed on the button itself and no assertion after the press
 * was ever reached. The locator is repaired; the defect it uncovered is what
 * these markers stand on. Delete the markers with the fix.
 */
codexTest.describe('Codex Interrupt', () => {
  codexTest.fixme('send a prompt and interrupt mid-response', async ({ authenticatedCodexWorkspace, page }) => {
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

  codexTest.fixme('interrupted turn shows completion status', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Write a 5000-word analysis of quantum computing with detailed chapters and citations.')

    const interruptBtn = page.locator('[data-testid="interrupt-button"]')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()

    // After interrupt, the thinking indicator must be gone.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })
})
