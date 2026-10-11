import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotGoalCycle } from './goalScenario'

copilotTest('sets, pauses, resumes and clears a native session goal', async ({ native }) => {
  await exerciseCopilotGoalCycle(native.page)
})
