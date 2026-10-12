import { exerciseModelError } from '../helpers/nativeModelError'
import { junieTest } from '../junie-fixtures'

junieTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
