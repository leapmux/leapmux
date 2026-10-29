import { codexTest } from '../codex-fixtures'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'

codexTest('reports a native model error and accepts the next turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

codexTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // The E2E configuration gives a stream failure one retry (`stream_max_retries = 1`
  // in `helpers/codexEnvironment.ts`), so the second cut stream ends the turn.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The stream was cut before its completion.',
      midStream: true,
    },
    attempts: 2,
  })
})
