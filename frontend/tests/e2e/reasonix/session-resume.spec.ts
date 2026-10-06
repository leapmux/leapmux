import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
