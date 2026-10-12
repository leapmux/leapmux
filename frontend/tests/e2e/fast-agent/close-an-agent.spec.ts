import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

fastAgentTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
