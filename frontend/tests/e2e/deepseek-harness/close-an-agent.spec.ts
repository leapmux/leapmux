import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

deepseekHarnessTest('stops the native process and its owned command processes', async ({ native }) => {
  await exerciseCloseAgent(native)
})
