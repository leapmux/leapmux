import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'

junieTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
