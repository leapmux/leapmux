import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'

junieTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
