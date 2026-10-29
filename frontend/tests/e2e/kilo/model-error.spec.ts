import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { kiloTest } from '../kilo-fixtures'

kiloTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

kiloTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Kilo 7.8.17 retries a 5xx five times, as OpenCode does. It waits as long as a
  // `retry-after` header states, so each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The server had an error while processing your request.',
      retryAfterSeconds: 1,
    },
    attempts: 6,
  })
})

kiloTest('stops a turn whose native retry waits for hours and schedules LeapMux\'s own retry', async ({ native }) => {
  // Kilo 7.8.17 waits as long as a `retry-after` header states, inside the open turn,
  // and states the instant of its next attempt. LeapMux ends a turn whose next
  // attempt lies more than 10 minutes ahead, and schedules its own retry for
  // that instant, which a restart keeps.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 429,
      code: 'rate_limit_exceeded',
      message: 'Rate limit reached for requests.',
      retryAfterSeconds: 5 * 60 * 60,
    },
    attempts: 1,
    retryKind: 'usage limit',
  })
})
