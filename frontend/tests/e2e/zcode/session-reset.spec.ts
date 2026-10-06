import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
