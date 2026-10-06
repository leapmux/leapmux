import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseOpencodeMcpInputLimit } from '../helpers/opencodeMcpLimit'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('returns the actual native MCP elicitation refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseOpencodeMcpInputLimit({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, { binaryName: 'opencode', configurationVariable: 'OPENCODE_CONFIG_CONTENT' })
})
