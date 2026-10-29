import { commandCodeTest } from '../command-code-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

commandCodeTest('shows the native model failure and accepts a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

commandCodeTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Command Code 1.88.0 sends a 5xx ten times in about 35 s, then ends the turn
  // with `run_error` and the status of the last attempt.
  await exerciseTransientModelFailure(native, {
    error: { status: 529, code: 'overloaded_error', message: 'Overloaded' },
    attempts: 10,
  })
})
