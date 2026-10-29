import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { kimiTest } from '../kimi-fixtures'

kimiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

kimiTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Kimi Code 2.3.1 sends a request ten times in each step. Without a
  // `retry-after` header its backoff starts at 0.5 s and doubles to 32 s, about
  // 127 s in all, which exceeds the test budget. It waits as long as the header
  // states instead, so each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 503,
      code: 'server_error',
      message: 'The service is temporarily unavailable.',
      retryAfterSeconds: 1,
    },
    attempts: 10,
  })
})
