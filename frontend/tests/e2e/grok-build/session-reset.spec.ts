import { grokTest } from '../grok-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

grokTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
