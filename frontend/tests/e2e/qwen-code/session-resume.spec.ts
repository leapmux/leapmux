import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { qwenTest } from '../qwen-fixtures'

qwenTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
