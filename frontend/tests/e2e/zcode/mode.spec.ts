import { expect } from '@playwright/test'
import { ZCODE_MODE } from '../../../src/generated/contracts/zcode-protocol'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, settingsBar, waitForNativeSettingsHydrated, waitForSettingsHydrated } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMode, exerciseZCodePlanAndYolo } from './modeScenario'

zcodeTest('the mode chip starts on Build and can switch to Plan and Yolo', async ({ native, page }) => {
  await expect(settingsBar(page)).toBeVisible()
  await expectSettingsChip(page, 'Build')
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(ZCODE_MODE))
  await exerciseZCodeMode(native, 'build')
  const planNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: 'plan',
    label: 'Plan',
    previousLabel: 'Build',
    noticeLabel: 'Mode',
  })
  await exerciseZCodeMode(native, 'plan')
  const yoloNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: 'yolo',
    label: 'Yolo',
    previousLabel: 'Plan',
    noticeLabel: 'Mode',
  })
  await exerciseZCodeMode(native, 'yolo')
  const buildNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: 'build',
    label: 'Build',
    previousLabel: 'Yolo',
    noticeLabel: 'Mode',
  })
  await exerciseZCodeMode(native, 'build')
  await page.reload()
  await waitForNativeSettingsHydrated(page)
  await expectSettingsChip(page, 'Build')
  await exerciseZCodeMode(native, 'build')
  await planNotice.assertStored()
  await yoloNotice.assertStored()
  await buildNotice.assertStored()
})

zcodeTest('plan mode refuses a native write that Yolo mode runs', async ({ native }) => {
  await exerciseZCodePlanAndYolo(native)
  await expectSettingsChip(native.page, 'Yolo')
})

// The exact menu leaves no room for `auto`.
zcodeTest('auto is not offered, because the shipped app-server does not implement it', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(ZCODE_MODE))
})
