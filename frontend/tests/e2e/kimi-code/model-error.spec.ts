import { exerciseModelError } from '../helpers/nativeModelError'
import { kimiTest } from '../kimi-fixtures'

kimiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
