import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { kimiTest } from '../kimi-fixtures'

kimiTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
