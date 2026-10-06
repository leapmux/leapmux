import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('closes the native agent and its actual owned process tree', async ({ native, page }) => {
  await exerciseCloseAgent(native, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
