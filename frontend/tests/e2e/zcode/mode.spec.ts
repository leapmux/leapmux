import { expect } from '@playwright/test'
import { ZCODE_MODE } from '../../../src/generated/contracts/zcode-protocol'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, settingsBar, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMode } from './modeScenario'

zcodeTest('the mode chip starts on Build and can switch to Plan and Yolo', async ({ native, page }) => {
  await expect(settingsBar(page)).toBeVisible()
  await expectSettingsChip(page, 'Build')
  await exerciseZCodeMode(native, 'build')
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  await exerciseZCodeMode(native, 'plan')
  await chooseSettingsOption(page, 'permissionMode-yolo')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Yolo')
  await exerciseZCodeMode(native, 'yolo')
  await chooseSettingsOption(page, 'permissionMode-build')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Build')
  await exerciseZCodeMode(native, 'build')
})

zcodeTest('plan mode refuses a native write that Yolo mode runs', async ({ native, page }) => {
  for (const mode of ['plan', 'yolo'] as const) {
    await chooseSettingsOption(page, `permissionMode-${mode}`)
    await waitForSettingsIdle(page)
    await exerciseZCodeMode(native, mode)
    await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${mode}`)
    await exerciseZCodeMode(native, mode)
  }
})

// The exact menu leaves no room for `auto`.
zcodeTest('auto is not offered, because the shipped app-server does not implement it', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(ZCODE_MODE))
})
