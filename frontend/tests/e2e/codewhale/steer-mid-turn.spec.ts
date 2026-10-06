import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('steers a queued message into the active turn', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await exerciseProviderSteer(page, modelScript, AgentProvider.CODEWHALE)
  })
})
