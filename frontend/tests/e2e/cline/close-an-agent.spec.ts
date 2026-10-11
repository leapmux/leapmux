import { clineTest } from '../cline-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

clineTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
