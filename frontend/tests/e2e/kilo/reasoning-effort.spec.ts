import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest('keeps its Plan mode and effort after a turn and reload', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, 'effort')
})

// Kilo resets the effort to a variant of the new model on each model write. Both models offer high.
kiloTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseModelSwitchKeepsOption({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, {
    kept: { groupId: 'effort', value: 'high' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'high' })
    },
  })
})
