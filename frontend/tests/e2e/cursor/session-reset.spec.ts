import { cursorTest } from '../cursor-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

cursorTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
