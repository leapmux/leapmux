import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('keeps the native plan command as text and offers no Plan mode', async ({ native }) => {
  await exerciseMissingNativePlanMode(native)
})
