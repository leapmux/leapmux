import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpencodeMcpInputLimit } from './mcpLimit'
import { nativeContext } from './scenarios'

opencodeTest('returns the actual native MCP elicitation refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseOpencodeMcpInputLimit(context, { binaryName: 'opencode', configurationVariable: 'OPENCODE_CONFIG_CONTENT' })
})
