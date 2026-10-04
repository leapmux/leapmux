import { commandCodeTest } from '../command-code-fixtures'
import { exerciseCommandCodePermissionLimit } from './permissionScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('blocks a native write without a dialog and applies bypass to the same session', async ({ defaultCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: defaultCommandCodeWorkspace.workspaceId })
  await exerciseCommandCodePermissionLimit(context)
})
