import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, REASONIX_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseReasonixSessionSettings({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX })
})

// Reasonix drops the effort override on a different model and notifies before it answers the model write.
// Both models offer low, medium, and high, and the default is high, so low differs from the reset.
reasonixTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseModelSwitchKeepsOption({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }, {
    kept: { groupId: 'effort', value: 'low' },
    model: REASONIX_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'low' })
    },
  })
})
