import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from '../opencode/settingsScenario'
import { KILO_PLAN_REMINDER } from './scenarios'

kiloTest('keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'effort', KILO_PLAN_REMINDER)
})

// Kilo resets the effort to a variant of the new model on each model write. Both models offer high.
kiloTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'high' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'high' })
    },
  })
})
