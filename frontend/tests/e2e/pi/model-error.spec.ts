import { exerciseModelError } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { piTest } from '../pi-fixtures'

piTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

piTest('retries a transient failure without a failed divider', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      { status: 503, message: '503 Service Unavailable' },
    ],
  })
})
