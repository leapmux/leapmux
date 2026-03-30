import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

const MODE_APPROVE = 'approve'

gooseTest.describe('Goose Settings', () => {
  gooseTest('mode and model can be changed live', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
    await page.locator(`[data-testid="permission-mode-${MODE_APPROVE}"]`).click()
    await expect(trigger).toContainText('Approve')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    const modelOption = page.locator('[data-testid^="model-"]').first()
    const modelName = await modelOption.textContent()
    await modelOption.click()
    if (modelName) {
      await expect(trigger).toContainText(modelName)
    }
  })
})
