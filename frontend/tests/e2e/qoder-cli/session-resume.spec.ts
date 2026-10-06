import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'

qoderTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
