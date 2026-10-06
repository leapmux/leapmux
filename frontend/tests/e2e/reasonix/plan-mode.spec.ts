import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixPlanAndNormalWrites } from './planWriteScenario'

reasonixTest('plan-mode: refuses a native write in Plan mode and asks in Normal mode', async ({ native }) => {
  await exerciseReasonixPlanAndNormalWrites(native)
})
