import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS, OPENCODE_FAMILY_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

opencodeTest('keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'effort')
})

// OpenCode resets the effort to its first variant when the model changes, and it sends the reset in a
// notification before it answers the model request. Both models offer high, which differs from that reset.
opencodeTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'variant:high' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'high' })
    },
  })
})

// Native clients keep an independent effort preference for each model. Returning to the reasoning model restores
// its previous choice after a model without variants hid the control.
opencodeTest('hides the effort for a model without variants and restores the previous model preference', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`,
    chosen: 'variant:low',
    via: OPENCODE_FAMILY_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'variant:low',
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
})
