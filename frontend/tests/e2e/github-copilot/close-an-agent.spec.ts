import { copilotTest } from '../copilot-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('closes the native agent and its actual owned process tree', async ({ native, page }) => {
  await exerciseCloseAgent(native, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
