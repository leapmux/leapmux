import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
