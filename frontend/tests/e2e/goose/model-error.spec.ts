import { gooseTest } from '../goose-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

// Goose 1.53.0 retries a failed model request three times, then states the error
// of its last attempt ("Ran into this error: ...") and ends the turn.
const GOOSE_REQUEST_ATTEMPTS = 4

gooseTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running', attempts: GOOSE_REQUEST_ATTEMPTS })
})
