import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { museTest } from '../muse-fixtures'

museTest('waits for the Worker close verdict and ends each owned process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
