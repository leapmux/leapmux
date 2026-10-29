import { exerciseModelError } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { museTest } from '../muse-fixtures'

museTest('shows the native model error and accepts the next valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

museTest('marks the cut attempt and folds the retry', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      {
        status: 503,
        message: 'The response ended early.',
        code: 'server_error',
        midStream: 'truncate',
      },
    ],
    failedAttempt: 'error',
  })
})
