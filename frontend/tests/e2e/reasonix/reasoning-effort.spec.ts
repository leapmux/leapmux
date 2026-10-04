import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseReasonixSessionSettings({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX })
})
