import { gooseTest } from '../goose-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

// Goose 1.53.0 retries a failed model request three times, then states the error
// of its last attempt ("Ran into this error: ...") and ends the turn. It retries
// a 4xx and a 5xx alike.
const GOOSE_REQUEST_ATTEMPTS = 4

gooseTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running', attempts: GOOSE_REQUEST_ATTEMPTS })
})

gooseTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Goose states the last 500 as "Ran into this error: Server error: ...", which LeapMux retries.
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message: 'The server had an error while processing your request.' },
    attempts: GOOSE_REQUEST_ATTEMPTS,
  })
})
