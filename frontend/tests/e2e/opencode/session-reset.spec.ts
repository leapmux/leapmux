import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
