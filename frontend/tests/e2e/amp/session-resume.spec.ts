import { ampTest } from '../amp-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

ampTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
