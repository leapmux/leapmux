import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { exerciseCopilotPlanAndEffort } from './settingsScenario'

copilotTest('keeps Plan mode and low effort after a turn and reload', async ({ native }) => {
  await exerciseCopilotPlanAndEffort(native, 'effort')
})

// Both models offer low. The runtime reports the tier that it runs, so the kept tier must come back after the switch.
copilotTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: MOCK_MODELS.gooseReasoning,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning })
      expect(JSON.stringify(request.body)).toMatch(/"(?:reasoning_effort|effort)":\s*"low"/)
    },
  })
})
