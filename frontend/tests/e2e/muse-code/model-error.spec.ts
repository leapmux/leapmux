import { exerciseModelError } from '../helpers/nativeModelError'
import { museTest } from '../muse-fixtures'

museTest('shows the native model error and accepts the next valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
