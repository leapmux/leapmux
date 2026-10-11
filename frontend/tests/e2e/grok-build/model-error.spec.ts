import { grokTest } from '../grok-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

grokTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
