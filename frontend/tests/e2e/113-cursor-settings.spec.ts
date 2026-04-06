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
    // The "auto" model is the default and may display as "default" in the trigger.
    // Select a non-default model first, then switch back to verify the change takes effect.
    const modelItems = page.locator('[data-testid^="model-"]')
    const count = await modelItems.count()
    if (count > 1) {
      // Click a non-auto model to trigger a visible change
      await modelItems.last().click()
      await waitForSettingsIdle(page)
      await openSettingsMenu(page)
    }
    await page.locator('[data-testid^="model-auto"]').first().click()
    await expect(trigger).toContainText('default')
  })
})
