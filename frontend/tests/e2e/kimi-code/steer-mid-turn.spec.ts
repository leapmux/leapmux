import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('steers a queued message into the active turn', async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await exerciseSteerBeforeTool(native)
  })
})
