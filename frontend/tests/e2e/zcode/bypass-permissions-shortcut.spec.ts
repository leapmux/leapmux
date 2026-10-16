import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { createNativePermissionRemoval } from '../helpers/nativePermissionRemoval'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodePlanAndYolo, exerciseZCodeShortcutOffer } from './modeScenario'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest('bypass-permissions-shortcut: the permission banner applies the selected bypass pill on allow', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: true })
})

zcodeTest('offers only the bypass permission shortcut', async ({ native }) => {
  await exerciseZCodeShortcutOffer(native)
})

zcodeTest('bypass-permissions-shortcut: plan mode refuses a native write that Yolo mode runs', async ({ native }) => {
  await exerciseZCodePlanAndYolo(native)
})

zcodeTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  const operations = await Promise.all([createNativePermissionRemoval(native), createNativePermissionRemoval(native)])
  await exerciseMidTurnBypass(native, operations)
})
