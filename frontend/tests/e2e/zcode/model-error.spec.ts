import { exerciseModelError } from '../helpers/nativeModelError'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
