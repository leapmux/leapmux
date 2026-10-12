import { droidTest } from '../droid-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

droidTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
