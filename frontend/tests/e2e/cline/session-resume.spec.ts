import { clineTest } from '../cline-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

clineTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
