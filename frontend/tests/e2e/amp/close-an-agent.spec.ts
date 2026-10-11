import { ampTest } from '../amp-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

ampTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
