import { geminiTest } from '../gemini-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

geminiTest('clears native context and preserves the saved transcript', async ({ native }) => {
  await exerciseSessionReset(native)
})
