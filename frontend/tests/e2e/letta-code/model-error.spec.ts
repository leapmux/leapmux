import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { lettaTest } from '../letta-fixtures'

lettaTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

lettaTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Letta Code 0.34.23 nests its retries: its provider layer sends a 503 four
  // times, and its turn loop runs that layer four times. It then ends the turn
  // with `llm_api_error`.
  await exerciseTransientModelFailure(native, {
    error: { status: 503, code: 'server_error', message: 'Service Unavailable' },
    attempts: 16,
  })
})
