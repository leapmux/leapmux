import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

codebuddyTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
