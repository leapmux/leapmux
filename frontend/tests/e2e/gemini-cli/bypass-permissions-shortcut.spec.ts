import { geminiTest } from '../gemini-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'

geminiTest('runs native tools without prompts after the bypass shortcut', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
