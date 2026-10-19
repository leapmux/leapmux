import { copilotTest } from '../copilot-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'

copilotTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

copilotTest('retries a cut stream without retaining the failed attempt', async ({ native }) => {
  await exerciseModelRetry(native, { errors: [{ status: 500, message: 'The stream broke.', midStream: true }] })
})
