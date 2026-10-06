import { kiloTest } from '../kilo-fixtures'
import { exerciseOpencodeMcpInputLimit } from '../opencode/mcpLimit'
import { nativeContext } from './scenarios'

kiloTest('returns the actual native MCP elicitation refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseOpencodeMcpInputLimit(context, { binaryName: 'kilo', configurationVariable: 'KILO_CONFIG_CONTENT' })
})
