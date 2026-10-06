import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeGoalCycle } from './goalScenario'

zcodeTest('session-goal-pause-and-resume: sets, pauses, resumes, and clears the native goal', async ({ native }) => {
  await exerciseZCodeGoalCycle(native)
})
