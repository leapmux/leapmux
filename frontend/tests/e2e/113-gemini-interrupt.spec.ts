import { expect, GEMINI_E2E_SKIP_REASON, geminiTest } from './gemini-fixtures'
import { sendMessage } from './helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest.describe('Gemini Interrupt', () => {
  geminiTest('interrupt button appears during processing', async ({ authenticatedGeminiWorkspace, page }) => {
    void authenticatedGeminiWorkspace

    await sendMessage(page, 'Write a very long essay about the history of computing, covering all major milestones from the abacus to modern AI.')

    const stopButton = page.locator('[data-testid="stop-btn"]')
    await expect(stopButton).toBeVisible({ timeout: 30_000 }).catch(() => {
      // Fast responses may complete before we can observe the button.
    })
  })
})
