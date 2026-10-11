import { gooseTest } from '../goose-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

gooseTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
