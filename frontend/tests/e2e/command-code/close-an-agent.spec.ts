import { commandCodeTest } from '../command-code-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

commandCodeTest('stops the actual provider and its owned command processes', async ({ native }) => {
  await exerciseCloseAgent(native)
})
