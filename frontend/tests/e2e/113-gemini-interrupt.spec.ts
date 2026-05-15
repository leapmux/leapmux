import { expect, GEMINI_E2E_SKIP_REASON, geminiTest } from './gemini-fixtures'
import { sendMessage } from './helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest.describe('Gemini Interrupt', () => {
  geminiTest('interrupt button appears during processing', async ({ authenticatedGeminiWorkspace, page }) => {
    void authenticatedGeminiWorkspace

    // Long prompt so the agent must be streaming for several seconds —
    // the stop button must appear during that window. If it never does
    // (regression: button never wired up, or button stays hidden), the
    // assertion must fail rather than be silently swallowed.
    await sendMessage(page, 'Write a very long essay about the history of computing, covering all major milestones from the abacus to modern AI. Aim for at least 3000 words across multiple chapters.')

    const stopButton = page.locator('[data-testid="stop-btn"]')
    await expect(stopButton).toBeVisible({ timeout: 30_000 })

    // Click the interrupt and confirm processing stops.
    await stopButton.click()
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible({ timeout: 30_000 })
  })
})
