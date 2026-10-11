import { ampTest } from '../amp-fixtures'
import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'

ampTest('keeps the native plan command as text and offers no Plan mode', async ({ native }) => {
  await exerciseMissingNativePlanMode(native)
})
