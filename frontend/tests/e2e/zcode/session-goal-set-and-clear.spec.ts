import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeGoalCycle } from './goalScenario'

zcodeTest('sets, pauses, resumes, and clears the native goal', async ({ native }) => {
  await exerciseZCodeGoalCycle(native)
})
