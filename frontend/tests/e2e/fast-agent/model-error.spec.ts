import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

// fast-agent 0.10.47 calls the model three times, 10 s and then 20 s apart, and
// its OpenAI client sends a 5xx twice more inside each call.
const FAST_AGENT_SERVER_ERROR_REQUESTS = 9

fastAgentTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

fastAgentTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // fast-agent states the last 500 as "I hit an internal error while calling the model: ... (status=500)".
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message: 'The server had an error while processing your request.' },
    attempts: FAST_AGENT_SERVER_ERROR_REQUESTS,
  })
})
