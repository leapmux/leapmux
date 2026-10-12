import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

fastAgentTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
