import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

codebuddyTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
