import { copilotTest } from '../copilot-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

copilotTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
