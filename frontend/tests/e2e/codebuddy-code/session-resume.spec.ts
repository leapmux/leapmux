import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

codebuddyTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
