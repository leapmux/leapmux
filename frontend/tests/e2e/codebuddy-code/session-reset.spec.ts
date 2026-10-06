import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

codebuddyTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
