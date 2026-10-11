import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalLifecycle } from './goalScenario'

reasonixTest('sets and clears a native Reasonix goal before and after cancellation', async ({ native }) => {
  await exerciseReasonixGoalLifecycle(native)
})
