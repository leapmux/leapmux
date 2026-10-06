import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code settings', () => {
  mimoTest('steers a queued message into the active turn', async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await exerciseSteerBeforeTool(native)
  })
})
