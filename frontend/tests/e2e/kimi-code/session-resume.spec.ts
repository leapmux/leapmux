import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { kimiTest } from '../kimi-fixtures'

kimiTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
