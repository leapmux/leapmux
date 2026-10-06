import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { kiloTest } from '../kilo-fixtures'

kiloTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native)
})
