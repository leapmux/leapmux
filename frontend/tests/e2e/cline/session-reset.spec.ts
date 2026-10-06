import { clineTest } from '../cline-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

clineTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
