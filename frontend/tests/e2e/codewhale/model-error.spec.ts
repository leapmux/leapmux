import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

codewhaleTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

codewhaleTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // The E2E configuration turns Codewhale's own retry off (`[retry] enabled = false`
  // in `helpers/codewhaleEnvironment.ts`), so the 500 ends the turn after one request
  // as "Server error (500): ...".
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message: 'The server had an error while processing your request.' },
    attempts: 1,
  })
})
