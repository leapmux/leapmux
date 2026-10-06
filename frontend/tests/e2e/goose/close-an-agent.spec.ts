import { gooseTest } from '../goose-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('closes the native agent and its actual owned process tree', async ({ native, page }) => {
  await exerciseCloseAgent(native, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
