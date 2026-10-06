import { diracTest } from '../dirac-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

diracTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
