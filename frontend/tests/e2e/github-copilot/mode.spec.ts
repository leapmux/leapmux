import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotPlanAndEffort } from './settingsScenario'

copilotTest('mode: keeps Plan mode and low effort after a turn and reload', async ({ native }) => {
  await exerciseCopilotPlanAndEffort(native, 'mode')
})
