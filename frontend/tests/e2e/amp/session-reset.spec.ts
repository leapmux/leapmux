import { ampTest } from '../amp-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

ampTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
