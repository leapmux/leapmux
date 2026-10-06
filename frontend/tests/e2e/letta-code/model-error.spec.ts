import { exerciseModelError } from '../helpers/nativeModelError'
import { lettaTest } from '../letta-fixtures'

lettaTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
