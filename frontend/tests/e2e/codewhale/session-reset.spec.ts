import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

codewhaleTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
