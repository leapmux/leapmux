import { exerciseModelError } from '../helpers/nativeModelError'
import { kiloTest } from '../kilo-fixtures'

kiloTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
