import { exerciseModelError } from '../helpers/nativeModelError'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
