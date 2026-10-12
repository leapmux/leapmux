import { commandCodeTest } from '../command-code-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

commandCodeTest('shows the native model failure and accepts a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
