import { commandCodeTest } from '../command-code-fixtures'
import { expectDeclinedToolRowAcrossReload } from '../helpers/nativePermission'
import { exerciseCommandCodePermissionLimit } from './permissionScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('blocks a native write without a dialog and applies bypass to the same session', async ({ refusingCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: refusingCommandCodeWorkspace.workspaceId })
  await exerciseCommandCodePermissionLimit(context)
  // Command Code refuses the write with its own hook, and the refused call reads declined with that refusal. The
  // scenario writes the refused call as `native-default-denial`.
  await expectDeclinedToolRowAcrossReload(context, 'native-default-denial', 'requires permissions')
})
