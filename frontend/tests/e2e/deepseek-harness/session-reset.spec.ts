import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

deepseekHarnessTest('clears native context and preserves stored Worker rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
