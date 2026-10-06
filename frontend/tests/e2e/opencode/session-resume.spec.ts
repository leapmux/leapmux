import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
