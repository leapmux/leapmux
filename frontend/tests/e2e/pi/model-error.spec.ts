import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { piTest } from '../pi-fixtures'

piTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

piTest('retries a transient failure without a failed divider', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      { status: 503, message: '503 Service Unavailable' },
    ],
  })
})

piTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Pi 1.4.0 retries a transient failure three times, 2, 4, and 8 s apart, then
  // ends the turn with `willRetry: false`.
  await exerciseTransientModelFailure(native, {
    error: { status: 529, code: 'overloaded_error', message: 'Overloaded' },
    attempts: 4,
  })
})
