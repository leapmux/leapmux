import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

deepseekHarnessTest('shows the native model failure and accepts a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
