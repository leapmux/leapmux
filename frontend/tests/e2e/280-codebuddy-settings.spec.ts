import { CODEBUDDY_EFFORT_LEVEL, CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  settingsGroupTrigger,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/**
 * 280 — CodeBuddy Code settings.
 *
 * CodeBuddy advertises four permission modes at startup (note 12 of the
 * feature matrix): Default, Accept Edits, Plan and Bypass Permissions. The
 * effort axis carries CodeBuddy's own six words, not Claude's set. The spec
 * lists the mode menu, switches the effort and the mode, and proves that a
 * reload keeps both.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('the mode menu lists the four advertised modes', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    for (const testId of [
      `permissionMode-${CODEBUDDY_MODE.Default}`,
      `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`,
      `permissionMode-${CODEBUDDY_MODE.Plan}`,
      `permissionMode-${CODEBUDDY_MODE.BypassPermissions}`,
    ]) {
      await expect(group.locator(`[data-testid="${testId}"] input[type="radio"]`)).toBeVisible()
    }
    // The fixture opens the agent in Bypass Permissions.
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.BypassPermissions}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
  })

  codebuddyTest('switches the effort and the mode, and keeps them after a reload', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Bypass Permissions')
    // The model axis is present; the account and the local model table decide
    // which models it lists, so the spec asserts the group and not a choice.
    await openSettingsMenu(page, 'model')
    await closeComposerMenus(page)

    await chooseSettingsOption(page, `effort-${CODEBUDDY_EFFORT_LEVEL.Low}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Accept Edits')
    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.AcceptEdits}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })
})
