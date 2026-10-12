import { droidTest } from '../droid-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

droidTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
