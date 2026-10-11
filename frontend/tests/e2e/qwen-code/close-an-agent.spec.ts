import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { qwenTest } from '../qwen-fixtures'

qwenTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
