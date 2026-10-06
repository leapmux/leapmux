import { exerciseModelError } from '../helpers/nativeModelError'
import { piTest } from '../pi-fixtures'

piTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
