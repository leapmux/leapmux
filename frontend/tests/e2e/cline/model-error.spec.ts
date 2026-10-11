import { clineTest } from '../cline-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

clineTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
