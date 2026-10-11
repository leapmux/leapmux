import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('steers a queued message into the active turn', async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await exerciseSteerBeforeTool(native)
  })
})
