import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
