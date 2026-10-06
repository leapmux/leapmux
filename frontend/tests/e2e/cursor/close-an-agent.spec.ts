import { cursorTest } from '../cursor-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

cursorTest('closes the Worker agent and stops its actual native shell process tree', async ({ native }) => {
  await exerciseCloseAgent(native)
})
