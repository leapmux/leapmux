import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native)
})
