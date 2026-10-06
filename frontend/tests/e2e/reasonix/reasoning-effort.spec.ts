import { expect } from '@playwright/test'
import { MOCK_MODELS, REASONIX_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest('applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

// Reasonix drops the effort override on a different model and notifies before it answers the model write.
// Both models offer low, medium, and high, and the default is high, so low differs from the reset.
reasonixTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: REASONIX_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'low' })
    },
  })
})
