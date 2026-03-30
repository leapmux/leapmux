import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor Settings', () => {
  cursorTest('mode and model can be changed live', async ({ authenticatedCursorWorkspace, page }) => {
    void authenticatedCursorWorkspace

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
