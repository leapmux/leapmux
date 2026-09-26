import {
  applyPermissionPreset,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  waitForSettingsHydrated,
} from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

/**
 * 299 — Letta Code bypass permissions shortcut.
 *
 * Letta's Unrestricted mode auto-approves every tool, which is what Bypass
 * means. The shortcut switches the permission-mode axis to Unrestricted, and
 * the chip follows.
 */
lettaTest.describe('Letta Code bypass permissions', () => {
  lettaTest('the Bypass shortcut switches the session to Unrestricted', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
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
