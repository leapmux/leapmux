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
    kept: { groupId: 'effort', value: 'high' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'high' })
    },
  })
})

// OpenCode 1.18.34 states no effort option for a model that declares no reasoning variant, so the control hides. When
// the reasoning model returns, OpenCode reports the variant that the session held, Low, and the ACP base shows it.
opencodeTest('hides the effort for a model without variants and keeps the chosen effort over the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`,
    chosen: 'low',
    via: OPENCODE_FAMILY_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'low',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' }),
  })
})
