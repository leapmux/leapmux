import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page }) => {
  void authenticatedReasonixWorkspace
  await waitForSettingsHydrated(page)

  await chooseSettingsOption(page, 'permissionMode-plan')
  await expectSettingsChip(page, 'Plan')
  await waitForSettingsIdle(page)

  await chooseSettingsOption(page, 'effort-high')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'High')

  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)

  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Plan')
  await expectSettingsChip(page, 'High')

  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Normal')
})
