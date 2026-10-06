import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodePlanAndYolo } from './modeScenario'

zcodeTest('plan-mode: plan mode refuses a native write that Yolo mode runs', async ({ native }) => {
  await exerciseZCodePlanAndYolo(native)
})
