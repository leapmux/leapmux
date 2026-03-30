import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'
import { expect, KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

const PRIMARY_AGENT_PLAN = 'plan'

kiloTest.describe('Kilo Settings', () => {
  kiloTest('primary agent and model can be changed live', async ({ authenticatedKiloWorkspace, page }) => {
    void authenticatedKiloWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
    await page.locator(`[data-testid="primary-agent-${PRIMARY_AGENT_PLAN}"]`).click()
    await expect(trigger).toContainText('Plan')
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
