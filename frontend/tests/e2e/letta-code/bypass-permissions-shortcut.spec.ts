import { applyPermissionPreset, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, waitForSettingsHydrated } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code bypass permissions', () => {
  lettaTest('the Bypass shortcut switches the session to Unrestricted', async ({ askingLettaWorkspace, page }) => {
    void askingLettaWorkspace
    await waitForSettingsHydrated(page)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await closeComposerMenus(page)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Unrestricted')

    // The mode group reflects the same value the shortcut set.
    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator('[data-testid="permissionMode-unrestricted"] input[type="radio"]')).toBeChecked()
    await closeComposerMenus(page)
  })
})
