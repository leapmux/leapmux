import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { qwenTest } from '../qwen-fixtures'

qwenTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

qwenTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})
