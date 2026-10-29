import { expect } from '@playwright/test'
import { exerciseModelError, exerciseTransientModelFailure, nativeErrorMarker } from '../helpers/nativeModelError'
import { messageContents } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'

qwenTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

qwenTest('shows no retry row for a failure that Qwen does not retry', async ({ native }) => {
  // Qwen Code fails a 401 at once. It still states the end of that one attempt
  // as a failed request, as it does for each attempt that it retries.
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    error: {
      status: 401,
      code: 'invalid_api_key',
      message: nativeErrorMarker(),
    },
  })
  await expect(messageContents(native.page).filter({ hasText: 'API retry' })).toHaveCount(0)
})

qwenTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Qwen Code retries no error frame that arrives inside a stream, and states its
  // text "Overloaded" as the failure of the prompt.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 529,
      code: 'overloaded_error',
      message: 'Overloaded',
      midStream: true,
    },
    attempts: 1,
  })
})
