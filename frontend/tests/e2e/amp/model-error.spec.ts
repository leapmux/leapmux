import { ampTest } from '../amp-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

ampTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

ampTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Amp's server runs the agent loop and its retries. The CLI replaces an error
  // that mentions an overload with its own "Model Provider Overloaded" text.
  await exerciseTransientModelFailure(native, {
    error: { status: 529, code: 'overloaded_error', message: 'The model provider is overloaded.' },
    attempts: 1,
  })
})
