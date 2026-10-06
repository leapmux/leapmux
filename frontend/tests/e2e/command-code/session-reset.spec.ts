import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

commandCodeTest('clears native context and preserves prior Worker rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
