import { geminiTest } from '../gemini-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'

geminiTest('runs native tools without prompts after the bypass shortcut', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

geminiTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})
