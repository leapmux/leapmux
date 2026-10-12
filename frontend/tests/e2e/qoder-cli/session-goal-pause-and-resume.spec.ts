import { qoderTest } from '../qoder-fixtures'
import { exerciseNativeGoalCycle } from './goalScenarios'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest('sets, pauses, resumes, and clears a native session goal', async ({ native }) => {
    await exerciseNativeGoalCycle(native)
  })
})
