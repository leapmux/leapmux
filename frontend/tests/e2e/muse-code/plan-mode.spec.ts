import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'
import { museTest } from '../muse-fixtures'

// Muse's approval vocabulary (allowAll, promptUnmatched, onRequest, denyUnmatched) states no
// planning mode, and its model tool catalog offers no plan entry tool.
museTest('keeps the native plan command as text and offers no Plan mode', async ({ native }) => {
  await exerciseMissingNativePlanMode(native)
})
