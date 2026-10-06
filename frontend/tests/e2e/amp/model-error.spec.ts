import { ampTest } from '../amp-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

ampTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
