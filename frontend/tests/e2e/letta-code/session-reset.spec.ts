import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'

lettaTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
