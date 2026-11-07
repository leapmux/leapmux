import { antigravityTest } from '../antigravity-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

antigravityTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
