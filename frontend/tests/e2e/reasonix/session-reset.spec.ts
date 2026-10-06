import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
