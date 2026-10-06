import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'

lettaTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
