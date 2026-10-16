import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { nativeContext } from './scenarios'

ohMyPiTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})

ohMyPiTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ askingOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingOhMyPiWorkspace.workspaceId })
  await exerciseMidTurnBypass(native)
})
