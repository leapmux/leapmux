import { diracTest } from '../dirac-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

diracTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
