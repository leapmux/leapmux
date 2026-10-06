import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { kiroTest } from '../kiro-fixtures'

kiroTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
