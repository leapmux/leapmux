import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { kimiTest } from '../kimi-fixtures'

kimiTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

kimiTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})
