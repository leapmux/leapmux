import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('steers a queued message into the active turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await exerciseProviderSteer(page, modelScript, AgentProvider.KIMI_CODE)
  })
})
