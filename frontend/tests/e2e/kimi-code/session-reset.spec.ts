import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { kimiTest } from '../kimi-fixtures'

kimiTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
