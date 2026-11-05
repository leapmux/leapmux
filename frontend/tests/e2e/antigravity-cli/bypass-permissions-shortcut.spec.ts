import { antigravityTest } from '../antigravity-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'

antigravityTest('runs native tools without prompts after the bypass shortcut', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

antigravityTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})
