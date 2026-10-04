import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { applyPermissionPreset, closeComposerMenus, openPlusMenu, openSettingsMenu, waitForSettingsHydrated } from '../helpers/ui'
import { expect, expectQoderModeChip, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('the Smart shortcut selects Auto mode', async ({ qoderWorkspace, page }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await closeComposerMenus(page)

    await applyPermissionPreset(page, 'smart')
    await expectQoderModeChip(page, 'Auto')
    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator(`[data-testid="permissionMode-${QODER_MODE.Auto}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
  })
})
