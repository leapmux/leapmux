import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'

qoderTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
