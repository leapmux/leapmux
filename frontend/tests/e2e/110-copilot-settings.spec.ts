import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

const MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'

copilotTest.describe('Copilot Settings', () => {
  copilotTest('mode and model can be changed live', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
    await page.locator(`[data-testid="permission-mode-${MODE_PLAN}"]`).click()
    await expect(trigger).toContainText('Plan')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await page.locator('[data-testid^="model-gpt-5.4-mini"]').first().click()
    await expect(trigger).toContainText('GPT-5.4 mini')
  })
})
