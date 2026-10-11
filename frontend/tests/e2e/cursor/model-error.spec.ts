import { cursorTest } from '../cursor-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

cursorTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
