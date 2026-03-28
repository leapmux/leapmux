import { expect, GEMINI_E2E_SKIP_REASON, geminiTest } from './gemini-fixtures'
import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest.describe('Gemini Settings', () => {
  geminiTest('permission mode and model can be changed live', async ({ authenticatedGeminiWorkspace, page }) => {
    void authenticatedGeminiWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await page.locator('[data-testid^="model-auto"]').first().click()
    await expect(trigger).toContainText('Auto')
  })
})
