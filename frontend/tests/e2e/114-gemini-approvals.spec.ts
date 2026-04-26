import { expect, GEMINI_E2E_SKIP_REASON, geminiTest } from './gemini-fixtures'
import { sendMessage, waitForControlBanner } from './helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest.describe('Gemini Approvals', () => {
  geminiTest('approval request renders in default mode', async ({ authenticatedGeminiWorkspace, page }) => {
    void authenticatedGeminiWorkspace

    await sendMessage(page, 'Run this exact command: rm -rf /tmp/gemini-approval-test-dir-nonexistent')

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('rm -rf /tmp/gemini-approval-test-dir-nonexistent')
    await expect(page.locator('[data-testid^="control-decision-"]').first()).toBeVisible()
  })
})
