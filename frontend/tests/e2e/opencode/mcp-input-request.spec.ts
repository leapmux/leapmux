import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeMcpInput } from './mcpLimit'
import { nativeContext } from './scenarios'

opencodeTest('answers a native MCP form and restores its draft after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await exerciseOpenCodeMcpInput(context, { binaryName: 'opencode', configurationVariable: 'OPENCODE_CONFIG_CONTENT' })
})
