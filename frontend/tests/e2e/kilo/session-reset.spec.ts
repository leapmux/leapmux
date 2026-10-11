import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { kiloTest } from '../kilo-fixtures'

kiloTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
