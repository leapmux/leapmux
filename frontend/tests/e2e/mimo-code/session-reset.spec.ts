import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'

mimoTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
