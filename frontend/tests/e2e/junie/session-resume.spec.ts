import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'

junieTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
