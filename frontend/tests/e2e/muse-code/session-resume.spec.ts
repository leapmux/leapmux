import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { museTest } from '../muse-fixtures'

museTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
