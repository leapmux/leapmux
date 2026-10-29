import { clineTest } from '../cline-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

clineTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

clineTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Cline 3.0.73 retries a 5xx five times. Without a `retry-after` header its
  // backoff waits 2, 4, 8, 16, and 32 s, about 62 s in all. It waits as long as
  // the header states, up to 60 s, so each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 503,
      code: 'server_error',
      message: 'Service Unavailable',
      retryAfterSeconds: 1,
    },
    attempts: 6,
  })
})
