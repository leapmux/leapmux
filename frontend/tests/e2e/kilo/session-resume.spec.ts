import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { kiloTest } from '../kilo-fixtures'

kiloTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
