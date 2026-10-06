import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'

lettaTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
