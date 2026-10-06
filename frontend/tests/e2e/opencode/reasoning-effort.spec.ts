import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

opencodeTest('keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'effort')
})

// OpenCode resets the effort to its first variant when the model changes, and it sends the reset in a
// notification before it answers the model request. Both models offer high, which differs from that reset.
opencodeTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'high' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'high' })
    },
  })
})
