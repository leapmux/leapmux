import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotPlanApproval } from './planScenario'

copilotTest('uses the native exit tool and resumes after plan approval', async ({ native }) => {
  await exerciseCopilotPlanApproval(native)
})
