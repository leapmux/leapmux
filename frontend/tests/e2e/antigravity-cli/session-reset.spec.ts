import { antigravityTest } from '../antigravity-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

antigravityTest('clears native context and preserves the saved transcript', async ({ native }) => {
  await exerciseSessionReset(native)
})
