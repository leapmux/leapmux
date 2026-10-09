import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { museTest } from '../muse-fixtures'

museTest('starts a new native context and keeps the saved transcript', async ({ native }) => {
  await exerciseSessionReset(native)
})
