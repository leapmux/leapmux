import { copilotTest } from '../copilot-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'

copilotTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

copilotTest('retries a cut stream without retaining the failed attempt', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      { status: 500, message: 'The stream broke.', midStream: true },
    ],
  })
})

copilotTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Copilot CLI sends an overloaded request once and retries it five times, in
  // about 13 s. It then fails the turn with a query error that states the status.
  await exerciseTransientModelFailure(native, {
    error: { status: 529, code: 'overloaded_error', message: 'The model is overloaded' },
    attempts: 6,
  })
})
