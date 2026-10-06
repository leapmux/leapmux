import { applyPermissionPreset, expectPermissionShortcuts, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodePlanAndYolo } from './modeScenario'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest('bypass-permissions-shortcut: the permission banner applies the selected bypass pill on allow', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: true })
})

zcodeTest('offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  // ZCode declares no Smart preset, so only the bypass shortcut is drawn.
  await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
})

zcodeTest('bypass-permissions-shortcut: plan mode refuses a native write that Yolo mode runs', async ({ native }) => {
  await exerciseZCodePlanAndYolo(native)
})
