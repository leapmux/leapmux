import { diracTest } from '../dirac-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

diracTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
