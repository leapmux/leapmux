import { commandCodeTest } from '../command-code-fixtures'
import { exerciseCommandCodePermissionLimit } from './permissionScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('blocks a native write without a dialog and applies bypass to the same session', async ({ refusingCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: refusingCommandCodeWorkspace.workspaceId })
  await exerciseCommandCodePermissionLimit(context)
})
