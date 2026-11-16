import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeCodeExecution } from './codeExecution'
import { nativeContext } from './scenarios'

opencodeTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await exerciseOpenCodeCodeExecution(context)
})
