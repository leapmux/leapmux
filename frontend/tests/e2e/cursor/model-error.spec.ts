import { cursorTest } from '../cursor-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

cursorTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

cursorTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // A gateway answers the 503 with a bare Connect `unavailable` error, which
  // Cursor states as "Error: RetriableError: [unavailable] ...". Cursor retries
  // nothing in ACP mode, so the turn sends one request.
  await exerciseTransientModelFailure(native, {
    error: { status: 503, message: 'The model service is temporarily unavailable.' },
    attempts: 1,
  })
})
