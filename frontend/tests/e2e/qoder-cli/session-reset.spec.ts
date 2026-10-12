import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'

qoderTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
