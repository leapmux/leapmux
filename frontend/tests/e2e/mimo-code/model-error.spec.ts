import { exerciseModelError } from '../helpers/nativeModelError'
import { mimoTest } from '../mimo-fixtures'

mimoTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
