import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openSettingsMenu,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

/**
 * 295 — Letta Code permission modes.
 *
 * Letta Code's modes are Standard, Accept Edits, Unrestricted and Strict
 * (matrix note 12). The axis maps onto `runtime_start.mode`; a change reaches
 * the running session and the chip follows the value.
 */
lettaTest.describe('Letta Code modes', () => {
  lettaTest('the mode menu lists Standard, Accept Edits, Unrestricted and Strict', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)

    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator('[data-testid="permissionMode-standard"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-acceptEdits"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-unrestricted"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-strict"] input[type="radio"]')).toBeVisible()
    await closeComposerMenus(page)
  })

  lettaTest('a mode change reaches the chip and survives a reload', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-standard')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Standard')

    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Accept Edits')
  })
})
