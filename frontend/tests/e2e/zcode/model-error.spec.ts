import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

zcodeTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // ZCode 3.16.3 sends a request eleven times, with a backoff that starts at 2 s
  // and doubles to 60 s, which exceeds the test budget. It waits as long as a
  // `retry-after` header states instead, so each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The server had an error while processing your request.',
      retryAfterSeconds: 1,
    },
    attempts: 11,
  })
})
