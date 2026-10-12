import { droidTest } from '../droid-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

droidTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
