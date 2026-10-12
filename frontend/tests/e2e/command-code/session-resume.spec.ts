import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

commandCodeTest('reopens the native session and restores its Worker transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
