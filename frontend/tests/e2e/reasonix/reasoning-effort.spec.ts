import { expect } from '@playwright/test'
import { MOCK_MODELS, REASONIX_ALT_MODEL_ID, REASONIX_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
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

// Reasonix 1.38.7 states no effort option for a provider that states no reasoning protocol, so the control hides. It
// drops the effort override on a model change, so the default model returns at Auto, which Reasonix runs at the
// `default_effort` of the provider, High. The menu of the default model must be the same before and after the trip.
reasonixTest('hides the effort for a model without reasoning and settles Auto after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: `deepseek/${MOCK_MODELS.deepseek}`,
    chosen: 'low',
    via: REASONIX_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'auto',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.deepseek, reasoning_effort: 'high' }),
  })
})
