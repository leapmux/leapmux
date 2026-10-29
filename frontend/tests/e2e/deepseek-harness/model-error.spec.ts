import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
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

deepseekHarnessTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // dsh 0.2.1-rc.2 retries a SERVER failure five times, 0.5 s doubling to 8 s,
  // then ends the turn with that failure.
  await exerciseTransientModelFailure(native, {
    error: { status: 529, code: 'overloaded_error', message: 'Overloaded' },
    attempts: 6,
  })
})
