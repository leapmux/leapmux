import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'

mimoTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
