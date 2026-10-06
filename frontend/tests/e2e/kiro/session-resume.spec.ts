import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { kiroTest } from '../kiro-fixtures'

kiroTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
