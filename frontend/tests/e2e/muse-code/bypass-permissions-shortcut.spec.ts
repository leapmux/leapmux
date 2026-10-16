import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('applies native allowAll before and after reload', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

museTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-promptUnmatched')
  await waitForSettingsIdle(native.page)
  await exerciseMidTurnBypass(native)
})
