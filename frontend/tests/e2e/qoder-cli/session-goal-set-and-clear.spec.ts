import { qoderTest } from '../qoder-fixtures'
import { exerciseNativeGoalCycle } from './goalScenarios'

qoderTest('sets and clears an actual native goal without a model turn', async ({ native }) => {
  await exerciseNativeGoalCycle(native)
})
