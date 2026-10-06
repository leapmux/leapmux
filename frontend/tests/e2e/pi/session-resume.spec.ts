import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
