import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { qwenTest } from '../qwen-fixtures'

qwenTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
