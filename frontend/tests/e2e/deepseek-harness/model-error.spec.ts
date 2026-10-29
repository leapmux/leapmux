import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'

deepseekHarnessTest('shows the native model failure and accepts a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

deepseekHarnessTest('folds native retries and resolves the retry row', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      { status: 503, message: 'Service unavailable' },
      { status: 503, message: 'Service unavailable' },
    ],
  })
})
