import { grokTest } from '../grok-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

grokTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
