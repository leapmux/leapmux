import { REASONIX_APPROVAL, REASONIX_CONFIG } from '../../../src/generated/contracts/reasonix-protocol'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { applyPermissionPreset, chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

// Reasonix 1.38 asks to leave Plan mode after each Plan-mode answer, also under the Bypass preset. The settings scenario
// answers each of those requests, so no request stays open across the Bypass change and the reload.
reasonixTest('bypass-permissions-shortcut: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('runs a real native write without a permission request after the Bypass shortcut', async ({ native }) => {
  const { page } = native
  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await applyPermissionPreset(page, 'bypass')
  await expectNativeOptionValue(native, REASONIX_CONFIG.ToolApproval, REASONIX_APPROVAL.Yolo)
  await exerciseNativeToolWrite(native, { permission: 'absent' })
})
