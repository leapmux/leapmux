import { droidTest } from '../droid-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

droidTest('closes the native provider and its owned tool process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
